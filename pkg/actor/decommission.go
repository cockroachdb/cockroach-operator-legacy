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

package actor

import (
	"context"
	"fmt"

	api "github.com/cockroachdb/cockroach-operator/apis/v1alpha1"
	"github.com/cockroachdb/cockroach-operator/pkg/clustersql"
	"github.com/cockroachdb/cockroach-operator/pkg/database"
	"github.com/cockroachdb/cockroach-operator/pkg/features"
	"github.com/cockroachdb/cockroach-operator/pkg/kube"
	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/cockroachdb/cockroach-operator/pkg/scale"
	"github.com/cockroachdb/cockroach-operator/pkg/utilfeature"
	"github.com/cockroachdb/errors"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	kubetypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newDecommission(cl client.Client, config *rest.Config, clientset kubernetes.Interface) Actor {
	return &decommission{
		action: newAction(nil, cl, config, clientset),
	}
}

// decommission performs the initialization of the new cluster
type decommission struct {
	action
}

// GetActionType returns  api.DecommissionAction used to set the cluster status errors
func (d decommission) GetActionType() api.ActionType {
	return api.DecommissionAction
}

func (d decommission) Act(ctx context.Context, cluster *resource.Cluster, log logr.Logger) error {
	log.V(DEBUGLEVEL).Info("check decommission opportunities")

	stsName := cluster.StatefulSetName()

	key := kubetypes.NamespacedName{
		Namespace: cluster.Namespace(),
		Name:      stsName,
	}
	ss := &appsv1.StatefulSet{}
	if err := d.client.Get(ctx, key, ss); err != nil {
		log.Error(err, "decommission failed to fetch statefulset")
		return kube.IgnoreNotFound(err)
	}
	status := &ss.Status

	if status.CurrentReplicas == 0 || status.CurrentReplicas < status.Replicas {
		log.V(WARNLEVEL).Info("decommission statefulset does not have all replicas up")
		return NotReadyErr{Err: errors.New("decommission statefulset does not have all replicas up")}
	}

	nodes := uint(cluster.Spec().Nodes)
	log.Info("replicas decommissioning", "status.CurrentReplicas", status.CurrentReplicas, "expected", cluster.Spec().Nodes)
	if status.CurrentReplicas <= cluster.Spec().Nodes {
		return nil
	}
	// test to see if we are running inside of Kubernetes
	// If we are running inside of k8s we will not find this file.
	runningInsideK8s := inK8s("/var/run/secrets/kubernetes.io/serviceaccount/token")

	serviceName := cluster.PublicServiceAddress()
	if runningInsideK8s {
		log.V(DEBUGLEVEL).Info("operator is running inside of kubernetes, connecting to service for db connection")
	} else {
		serviceName = fmt.Sprintf("%s-0.%s.%s", cluster.Name(), cluster.Name(), cluster.Namespace())
		log.V(DEBUGLEVEL).Info("operator is NOT inside of kubernetes, connecting to pod ordinal zero for db connection")
	}

	// The connection needs to use the discovery service name because of the
	// hostnames in the SSL certificates
	conn := &database.DBConnection{
		Ctx:              ctx,
		Client:           d.client,
		RestConfig:       d.config,
		ServiceName:      serviceName,
		Namespace:        cluster.Namespace(),
		DatabaseName:     "system", // TODO we need to use variable instead of string
		Port:             cluster.Spec().SQLPort,
		RunningInsideK8s: runningInsideK8s,
	}

	// see https://github.com/cockroachdb/cockroach-operator/issues/204 for above TODO
	if cluster.Spec().TLSEnabled {
		conn.UseSSL = true
		conn.ClientCertificateSecretName = cluster.ClientTLSSecretName("root")
		conn.RootCertificateSecretName = cluster.NodeTLSSecretName()
	}
	db, err := database.NewDbConnection(conn)
	if err != nil {
		return errors.Wrapf(err, "failed to create database connection")
	}
	log.V(DEBUGLEVEL).Info("opened db connection")
	defer db.Close()

	timeout, err := clustersql.RangeMoveDuration(ctx, db)
	if err != nil {
		return errors.Wrap(err, "failed to get range move duration")
	}

	drainer := scale.NewCockroachNodeDrainer(log, cluster.Namespace(), ss.Name, d.config, d.clientset, cluster.Spec().TLSEnabled, 3*timeout)
	pvcPruner := scale.PersistentVolumePruner{
		Namespace:   cluster.Namespace(),
		StatefulSet: ss.Name,
		ClientSet:   d.clientset,
		Logger:      log,
	}
	//we should start scale down
	scaler := scale.Scaler{
		Logger: log,
		CRDB: &scale.CockroachStatefulSet{
			ClientSet: d.clientset,
			Namespace: cluster.Namespace(),
			Name:      ss.Name,
		},
		Drainer:   drainer,
		PVCPruner: &pvcPruner,
	}
	if err := scaler.EnsureScale(ctx, nodes, *cluster.Spec().GRPCPort, utilfeature.DefaultMutableFeatureGate.Enabled(features.AutoPrunePVC)); err != nil {
		/// now check if the decommissionStaleErr and update status
		log.Error(err, "decommission failed")
		cluster.SetFalse(api.DecommissionCondition)
		return err
	}
	// TO DO @alina we will need to save the status foreach action
	cluster.SetTrue(api.DecommissionCondition)
	log.V(DEBUGLEVEL).Info("decommission completed", "cond", ss.Status.Conditions)
	return nil
}
