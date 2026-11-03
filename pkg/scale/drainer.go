/*
Copyright 2026 The Cockroach Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scale

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/cockroachdb/errors"
	"github.com/go-logr/logr"
	"go.uber.org/zap/zapcore"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var (
	// ErrDecommissioningStalled indicates that the decommissioning process of
	// a node has stalled due to the KV allocator being unable to relocate ranges
	// to another node. This could happen if no nodes have available disk space
	// or if ZONE CONFIGURATION constraints can not be satisfied.
	ErrDecommissioningStalled = errors.New("decommissioning has stalled")
)

// Drainer interface
type Drainer interface {
	Decommission(ctx context.Context, replica uint, gRPCPort int32) error
}

// CockroachNodeDrainer does decommissioning of nodes in the CockroachDB cluster
type CockroachNodeDrainer struct {
	Secure   bool
	Logger   logr.Logger
	Executor *CockroachExecutor
	// RangeRelocationTimeout is the maximum amount of time to wait
	// for a range to move. If no ranges have moved from the draining
	// node in the given durration Decommission will fail with
	// ErrDecommissioningStalled
	RangeRelocationTimeout time.Duration
}

// NewCockroachNodeDrainer ctor
func NewCockroachNodeDrainer(logger logr.Logger, namespace, ssname string, config *rest.Config, clientset kubernetes.Interface, secure bool, rangeRelocation time.Duration) Drainer {
	return &CockroachNodeDrainer{
		Secure:                 secure,
		Logger:                 logger,
		RangeRelocationTimeout: rangeRelocation,
		Executor: &CockroachExecutor{
			Namespace:   namespace,
			StatefulSet: ssname,
			Config:      config,
			ClientSet:   clientset,
		},
	}
}

// Decommission commands the node to start training process and watches for it to complete or fail after timeout
func (d *CockroachNodeDrainer) Decommission(ctx context.Context, replica uint, gRPCPort int32) error {
	lastNodeID, err := d.findNodeID(ctx, replica, d.Executor.StatefulSet)
	if err != nil {
		return err
	}

	d.Logger.V(int(zapcore.InfoLevel)).Info("draining node", "NodeID", lastNodeID)

	if err := d.executeDrainCmd(ctx, lastNodeID, gRPCPort); err != nil {
		return err
	}

	check := d.makeDrainStatusChecker(lastNodeID)

	lastCheckTime := time.Now()
	lastCheckReplicas, err := check(ctx)
	if err != nil {
		return err
	}

	f := func() error {
		replicas, err := check(ctx)
		if err != nil {
			return err
		}

		// Node has finished draining successfully
		if replicas == 0 {
			return d.markNodeAsDecommissioned(ctx, lastNodeID, gRPCPort)
		}

		// If no replicas have been moved within our timeout, assume that the KV allocator
		// is unable to relocate ranges any more. This could happen for a variety of reasons,
		// namely disk space constraints or constraints due to ZONE CONFIGURATIONS.
		if lastCheckReplicas == replicas && time.Since(lastCheckTime) > d.RangeRelocationTimeout {
			return backoff.Permanent(errors.Wrapf(
				ErrDecommissioningStalled,
				"no ranges moved in %s",
				d.RangeRelocationTimeout,
			))
		}

		// Only update last check timestamp if the # of replicas
		// has changed to keep our check as aggressive and correct
		// as possible.
		if lastCheckReplicas != replicas {
			lastCheckTime = time.Now()
			lastCheckReplicas = replicas
		}

		// MaxElapsedTime for this backoff is infinite, this error should never
		// be returned to the caller. If you happen to see this error, the
		// running code is either outdated or something terrible has happened.
		return fmt.Errorf("node %d has not completed draining yet", lastNodeID)
	}

	b := backoff.NewExponentialBackOff()
	b.MaxInterval = d.RangeRelocationTimeout
	// 0 disabled MaxElapsedTime, we're relying on RangeRelocationTimeout to
	// cancel this backoff loop if it is required. A clusters that contains
	// terrabytes of ranges may take a day or two to full decommission. As long
	// as ranges are moving within our timeout, the operation is still healthy.
	b.MaxElapsedTime = 0
	return backoff.Retry(f, b)
}

func (d *CockroachNodeDrainer) makeDrainStatusChecker(id uint) func(ctx context.Context) (uint64, error) {
	cmd := []string{
		"./cockroach", "node", "status", fmt.Sprintf("%d", id),
		"--decommission", "--format=csv",
	}

	if d.Secure {
		cmd = append(cmd, "--certs-dir=cockroach-certs")
	} else {
		cmd = append(cmd, "--insecure")
	}

	return func(ctx context.Context) (uint64, error) {
		stdout, _, err := d.Executor.Exec(ctx, 0, cmd)
		if err != nil {
			return 0, err
		}

		r := csv.NewReader(strings.NewReader(stdout))

		header, err := r.Read()
		if err != nil {
			return 0, errors.Wrap(err, "failed to read CSV header")
		}

		record, err := r.Read()
		if err != nil {
			return 0, errors.Wrapf(err, "failed to get node draining status, id=%d", id)
		}

		columns := make(map[string]int, len(header))
		for i, name := range header {
			columns[name] = i
		}

		getColumn := func(name string) (string, error) {
			index, ok := columns[name]
			if !ok {
				return "", errors.Errorf("missing column %s", name)
			}

			if index >= len(record) {
				return "", errors.Errorf("missing value for column %s", name)
			}

			return record[index], nil
		}

		isLive, err := getColumn("is_live")
		if err != nil {
			return 0, err
		}

		replicasStr, err := getColumn("gossiped_replicas")
		if err != nil {
			return 0, err
		}

		isDecommissioning, err := getColumn("is_decommissioning")
		if err != nil {
			return 0, err
		}

		d.Logger.V(int(zapcore.InfoLevel)).Info(
			"draining node due to decommission",
			"id", id,
			"isLive", isLive,
			"replicas", replicasStr,
			"isDecommissioning", isDecommissioning,
		)

		if isLive != "true" || isDecommissioning != "true" {
			return 0, errors.New("unexpected node status")
		}

		replicas, err := strconv.ParseUint(replicasStr, 10, 64)
		if err != nil {
			return 0, errors.Wrap(err, "failed to parse replicas number")
		}

		return replicas, nil
	}
}

func (d *CockroachNodeDrainer) executeDrainCmd(ctx context.Context, id uint, gRPCPort int32) error {
	cmd := []string{
		"./cockroach", "node", "decommission", fmt.Sprintf("%d", id), "--wait=none", fmt.Sprintf("--port=%d", gRPCPort),
	}

	if d.Secure {
		cmd = append(cmd, "--certs-dir=cockroach-certs")
	} else {
		cmd = append(cmd, "--insecure")
	}

	if _, _, err := d.Executor.Exec(ctx, 0, cmd); err != nil {
		return errors.Wrapf(err, "failed to start draining node %d", id)
	}

	return nil
}

// markNodeAsDecommissioned sets a node as `decommissioned`. This is the final step in decommissioning
// a node which will transition it from `decommissioning` to `decommissioned`. This should be executed
// after it's confirmed that there are 0 replicas on the node.
func (d *CockroachNodeDrainer) markNodeAsDecommissioned(ctx context.Context, id uint, gRPCPort int32) error {
	cmd := []string{"./cockroach", "node", "decommission", fmt.Sprintf("%d", id), fmt.Sprintf("--port=%d", gRPCPort)}

	if d.Secure {
		cmd = append(cmd, "--certs-dir=cockroach-certs")
	} else {
		cmd = append(cmd, "--insecure")
	}

	if _, _, err := d.Executor.Exec(ctx, 0, cmd); err != nil {
		return errors.Wrapf(err, "failed to mark node as decommissioned: node: %d", id)
	}

	return nil
}

func (d *CockroachNodeDrainer) findNodeID(ctx context.Context, replica uint, stsName string) (uint, error) {
	cmd := []string{"./cockroach", "node", "status", "--format=csv"}

	if d.Secure {
		cmd = append(cmd, "--certs-dir=cockroach-certs")
	} else {
		cmd = append(cmd, "--insecure")
	}

	stdout, _, err := d.Executor.Exec(ctx, 0, cmd)
	if err != nil {
		return 0, err
	}

	host := fmt.Sprintf("%s-%d.%s.%s", stsName,
		replica, stsName, d.Executor.Namespace)
	r := csv.NewReader(strings.NewReader(stdout))
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, errors.Wrap(err, "failed to find last node id")
		}

		idStr, address := record[0], record[1]
		if strings.Contains(address, host) {
			id, err := strconv.ParseUint(idStr, 10, 32)
			if err != nil {
				return 0, errors.Wrap(err, "failed to extract node id from string")
			}

			return uint(id), nil
		}
	}

	return 0, fmt.Errorf("could not find the id of replica %d", replica)
}
