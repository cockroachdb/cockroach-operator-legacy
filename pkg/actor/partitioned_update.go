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
	"os"
	"time"

	"github.com/Masterminds/semver/v3"
	api "github.com/cockroachdb/cockroach-operator/apis/v1alpha1"
	"github.com/cockroachdb/cockroach-operator/pkg/database"
	"github.com/cockroachdb/cockroach-operator/pkg/healthchecker"
	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/cockroachdb/cockroach-operator/pkg/update"
	"github.com/cockroachdb/errors"
	"github.com/go-logr/logr"
	"go.uber.org/zap/zapcore"
	appsv1 "k8s.io/api/apps/v1"
	kubetypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newPartitionedUpdate(cl client.Client, config *rest.Config, clientset kubernetes.Interface) Actor {
	return &partitionedUpdate{
		action: newAction(nil, cl, config, clientset),
	}
}

// upgrade handles minor and major version upgrades without finalization
type partitionedUpdate struct {
	action
}

// GetActionType returns api.PartitionedUpdateAction action used to set the cluster status errors
func (up *partitionedUpdate) GetActionType() api.ActionType {
	return api.PartitionedUpdateAction
}

// Act runs a new partitionUpdate.
// This update pattern handles the sql calls and workflow in order to
// update a cr cluster.  This is replacing the old update actor.
func (up *partitionedUpdate) Act(ctx context.Context, cluster *resource.Cluster, log logr.Logger) error {

	// TODO we have edge cases that we are not covering
	// see https://github.com/cockroachdb/cockroach-operator/issues/202

	log.V(DEBUGLEVEL).Info("checking update opportunities, using a partitioned update")

	stsName := cluster.StatefulSetName()

	key := kubetypes.NamespacedName{
		Namespace: cluster.Namespace(),
		Name:      stsName,
	}
	statefulSet := &appsv1.StatefulSet{}
	if err := up.client.Get(ctx, key, statefulSet); err != nil {
		return errors.Wrap(err, "failed to fetch statefulset")
	}

	if statefulSetIsUpdating(statefulSet) {
		return NotReadyErr{Err: errors.New("statefulset is updating, waiting for the update to finish")}
	}

	// TODO we are relying on the container name for more than one purpose
	// it tells us the version and also we are finding it by name
	// We need to have it not tell us the version
	// See https://github.com/cockroachdb/cockroach-operator/issues/200

	containerWanted := cluster.GetAnnotationContainerImage()
	if containerWanted == "" {
		cluster.SetFalse(api.CrdbVersionChecked)
		log.Info("no crdbcontainerimage annotation found ... waiting for version checker to run")
		return nil
	}

	versionWantedCalFmtStr := cluster.GetVersionAnnotation()
	if versionWantedCalFmtStr == "" {
		cluster.SetFalse(api.CrdbVersionChecked)
		log.V(DEBUGLEVEL).Info("no version annotation found on crd ... waiting for version checker to run")
		return nil
	}
	currentVersionCalFmtStr := statefulSet.Annotations[resource.CrdbVersionAnnotation]
	if currentVersionCalFmtStr == "" {
		cluster.SetFalse(api.CrdbVersionChecked)
		log.Info("no version annotation found on sts ... waiting for version checker to run")
		return nil
	}

	// check annotation
	if currentVersionCalFmtStr == versionWantedCalFmtStr {
		log.Info("no version changes needed")
		return nil
	}

	currentVersion, err := semver.NewVersion(currentVersionCalFmtStr)
	if err != nil {
		return errors.Wrapf(err, "failed to parse container image version: %s", currentVersionCalFmtStr)
	}
	wantVersion, err := semver.NewVersion(versionWantedCalFmtStr)
	if err != nil {
		return errors.Wrapf(err, "failed to parse spec image version: %s", versionWantedCalFmtStr)
	}

	// TODO we probably should make these items and more configurable
	// see https://github.com/cockroachdb/cockroach-operator/issues/203
	podUpdateTimeout := 10 * time.Minute
	podMaxPollingInterval := 30 * time.Minute

	// test to see if we are running inside of Kubernetes
	// If we are running inside of k8s we will not find this file.
	runningInsideK8s := inK8s("/var/run/secrets/kubernetes.io/serviceaccount/token")

	serviceName := cluster.PublicServiceAddress()
	if runningInsideK8s {
		log.V(DEBUGLEVEL).Info("operator is running inside of kubernetes, connecting to service for db connection")
	} else {
		serviceName = fmt.Sprintf("%s-0.%s.%s", cluster.Name(), cluster.Name(), cluster.Namespace())
		log.V(DEBUGLEVEL).Info("operator is NOT inside of kubernetes, connnecting to pod ordinal zero for db connection")
	}

	// The connection needs to use the discovery service name because of the
	// hostnames in the SSL certificates
	conn := &database.DBConnection{
		Ctx:              ctx,
		Client:           up.client,
		RestConfig:       up.config,
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

	// TODO we may have an error case where the operator will not finish an update, but will
	// still try to make a database connection.
	// see https://github.com/cockroachdb/cockroach-operator/issues/205

	// Create a new database connection for the update.
	// TODO we may want to create this db connection later
	// see https://github.com/cockroachdb/cockroach-operator/issues/207

	db, err := database.NewDbConnection(conn)
	if err != nil {
		return errors.Wrapf(err, "failed to create database connection")
	}
	log.V(int(zapcore.DebugLevel)).Info("opened db connection")
	defer db.Close()

	// TODO test downgrades
	// see https://github.com/cockroachdb/cockroach-operator/issues/208
	healthChecker := healthchecker.NewHealthChecker(cluster, up.clientset, up.config)
	log.V(int(zapcore.InfoLevel)).Info("update starting with partitioned update", "old version", currentVersionCalFmtStr, "new version", versionWantedCalFmtStr, "image", containerWanted)

	updateRoach := &update.UpdateRoach{
		CurrentVersion: currentVersion,
		WantVersion:    wantVersion,
		WantImageName:  containerWanted,
		StsName:        stsName,
		StsNamespace:   cluster.Namespace(),
		Db:             db,
	}

	k8sCluster := &update.UpdateCluster{
		Clientset:             up.clientset,
		PodUpdateTimeout:      podUpdateTimeout,
		PodMaxPollingInterval: podMaxPollingInterval,
		HealthChecker:         healthChecker,
	}

	err = update.UpdateClusterCockroachVersion(
		ctx,
		updateRoach,
		k8sCluster,
		log,
	)

	// TODO set status so that we will not try to update the cluster again
	// TODO set status to rollback cluster?
	// This work is pending the status field updates
	// see https://github.com/cockroachdb/cockroach-operator/issues/209

	if err != nil {
		return errors.Wrapf(err, "failed to update sts with partitioned update: %s", stsName)
	}

	// TODO set status that we are completed.
	log.V(DEBUGLEVEL).Info("update completed with partitioned update", "new version", versionWantedCalFmtStr)
	return nil
}

// inK8s checks to see if the a file exists
func inK8s(file string) bool {
	_, err := os.Stat(file)
	return !os.IsNotExist(err)
}

func statefulSetIsUpdating(ss *appsv1.StatefulSet) bool {
	if ss.Status.ObservedGeneration == 0 {
		return false
	}

	if ss.Status.CurrentRevision != ss.Status.UpdateRevision {
		return true
	}

	if ss.Generation > ss.Status.ObservedGeneration && *ss.Spec.Replicas == ss.Status.Replicas {
		return true
	}

	return false
}
