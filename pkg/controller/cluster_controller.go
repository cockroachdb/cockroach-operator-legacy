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

package controller

import (
	"context"
	"fmt"
	"time"

	api "github.com/cockroachdb/cockroach-operator/apis/v1alpha1"
	"github.com/cockroachdb/cockroach-operator/pkg/actor"
	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/cockroachdb/cockroach-operator/pkg/util"
	"github.com/cockroachdb/errors"
	"github.com/go-logr/logr"
	"github.com/lithammer/shortuuid/v3"
	"go.uber.org/zap/zapcore"
	appsv1 "k8s.io/api/apps/v1"
	kbatch "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
	"k8s.io/api/networking/v1beta1"
	policy "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ClusterReconciler reconciles a CrdbCluster object
type ClusterReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Director actor.Director
}

// Note: you need a blank line after this list in order for the controller to pick this up.

// +kubebuilder:rbac:groups=crdb.cockroachlabs.com,resources=crdbclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=crdb.cockroachlabs.com,resources=crdbclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=crdb.cockroachlabs.com,resources=crdbclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/approval,verbs=update
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=services/finalizers,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;delete;deletecollection
// +kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups=core,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=list;update;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;create;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;create;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;create;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets/scale,verbs=get;watch;update
// +kubebuilder:rbac:groups=apps,resources=statefulsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets/finalizers,verbs=get;list;watch
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets/status,verbs=get
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets/finalizers,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses/status,verbs=get
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses/finalizers,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
// +kubebuilder:rbac:groups=batch,resources=jobs/finalizers,verbs=get;list;watch
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=mutatingwebhookconfigurations,verbs=get;update;patch;
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations,verbs=get;update;patch;
// +kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=use

// Reconcile is the reconciliation loop entry point for cluster CRDs.  It fetches the current cluster resources
// and uses its state to interact with the world via a set of actions implemented by `Actor`s
// (i.e. init cluster, create a statefulset).
// Each action can result in:
//   - a short requeue (5 seconds)
//   - a long requeue (5 minutes)
//   - cancel the loop and wait for another event
//   - if no other errors occurred continue to the next action
func (r *ClusterReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {

	// TODO should we make this configurable?
	// Ensure the loop does not take longer than 4 hours
	ctx, cancel := context.WithTimeout(ctx, 10*time.Hour)
	defer cancel()

	log := r.Log.WithValues("CrdbCluster", req.NamespacedName, "ReconcileId", shortuuid.New())
	log.V(int(zapcore.InfoLevel)).Info("reconciling CockroachDB cluster")

	fetcher := resource.NewKubeFetcher(ctx, req.Namespace, r.Client)

	cr := resource.ClusterPlaceholder(req.Name)
	if err := fetcher.Fetch(cr); err != nil {
		log.Error(err, "failed to retrieve CrdbCluster resource")
		return requeueIfError(client.IgnoreNotFound(err))
	}

	cluster := resource.NewCluster(cr)
	cluster.Fetcher = fetcher
	cleanClusterObj := cluster.Unwrap()
	// on first run we need to save the status and exit to pass Openshift CI
	// we added a state called Starting for field ClusterStatus to accomplish this
	if cluster.Status().ClusterStatus == "" {
		cluster.SetClusterStatusOnFirstReconcile()
		if err := r.updateClusterStatus(ctx, log, &cluster, cleanClusterObj); err != nil {
			log.Error(err, "failed to update cluster status")
			return requeueIfError(err)
		}
		return requeueImmediately()
	}

	//force version validation on mismatch between status and spec
	if cluster.True(api.CrdbVersionChecked) {
		if cluster.GetCockroachDBImageName() != cluster.Status().CrdbContainerImage {
			cluster.SetFalse(api.CrdbVersionChecked)
			if err := r.updateClusterStatus(ctx, log, &cluster, cleanClusterObj); err != nil {
				log.Error(err, "failed to update cluster status")
				return requeueIfError(err)
			}
			return requeueImmediately()
		}
	}

	// If the cluster is migrating, we stop reconciliation
	if val, ok := cluster.Unwrap().Labels[api.CrdbOperatorMigrationLabel]; ok && val == "true" {
		log.Info("cluster is migrating, stopping reconciliation")
		return noRequeue()
	}

	actorToExecute, err := r.Director.GetActorToExecute(ctx, &cluster, log)
	if err != nil {
		return requeueAfter(30*time.Second, nil)
	} else if actorToExecute == nil {
		log.Info("No actor to run; not requeueing")
		return noRequeue()
	}

	log.Info(fmt.Sprintf("Running action with name: %s", actorToExecute.GetActionType()))
	if err := actorToExecute.Act(ctx, &cluster, log); err != nil {
		// Save the error on the Status for each action
		log.Info("Error on action", "Action", actorToExecute.GetActionType(), "err", err.Error())
		cluster.SetActionFailed(actorToExecute.GetActionType(), err.Error())

		defer func(ctx context.Context, cluster *resource.Cluster) {
			if err := r.updateClusterStatus(ctx, log, cluster, cleanClusterObj); err != nil {
				log.Error(err, "failed to update cluster status")
			}
		}(ctx, &cluster)

		// Short pause
		var notReadyErr actor.NotReadyErr
		if errors.As(err, &notReadyErr) {
			log.V(int(zapcore.DebugLevel)).Info("requeueing", "reason", notReadyErr.Error(), "Action", actorToExecute.GetActionType())
			return requeueAfter(5*time.Second, nil)
		}

		// Long pause
		var cantRecoverErr actor.PermanentErr
		if errors.As(err, &cantRecoverErr) {
			log.Error(cantRecoverErr, "can't proceed with reconcile", "Action", actorToExecute.GetActionType())
			return noRequeue()
		}

		// No requeue until the user makes changes
		var validationErr actor.ValidationError
		if errors.As(err, &validationErr) {
			log.Error(validationErr, "can't proceed with reconcile")
			return noRequeue()
		}

		log.Error(err, "action failed")
		return requeueIfError(err)
	}

	// reset errors on each run  if there was an error,
	// this is to cover the not ready case
	if cluster.Failed(actorToExecute.GetActionType()) {
		cluster.SetActionFinished(actorToExecute.GetActionType())
	}

	if err := r.updateClusterStatus(ctx, log, &cluster, cleanClusterObj); err != nil {
		log.Error(err, "failed to update cluster status")
		return requeueIfError(err)
	}

	// Requeue so the director can pick the next action. Actions that change
	// neither the status nor a watched resource (SetupRBAC) produce no event of
	// their own, so without this the cluster stalls until the next cache resync.
	// Once no action is left, the director returns nil and the loop stops above.
	log.V(int(zapcore.InfoLevel)).Info("reconciliation completed")
	return requeueImmediately()
}

// updateClusterStatus preprocesses a cluster's Status and then persists it to
// the Kubernetes API. updateClusterStatus will retry on conflict errors.
func (r *ClusterReconciler) updateClusterStatus(ctx context.Context, log logr.Logger, cluster *resource.Cluster,
	cleanObj *api.CrdbCluster) error {
	cluster.SetClusterStatus()
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		return r.Client.Status().Patch(ctx, cluster.Unwrap(), client.MergeFrom(cleanObj))
	})
}

// SetupWithManager registers the controller with the controller.Manager from controller-runtime
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	var ingress client.Object
	ingress = &v1.Ingress{}
	if !util.CheckIfAPIVersionKindAvailable(mgr.GetConfig(), "networking.k8s.io/v1", "Ingress") {
		ingress = &v1beta1.Ingress{}
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&api.CrdbCluster{}).
		Owns(&corev1.Service{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&policy.PodDisruptionBudget{}).
		Owns(&kbatch.Job{}).
		Owns(ingress).
		Complete(r)
}

// InitClusterReconciler returns a registrator for new controller instance with the default logger
func InitClusterReconciler() func(ctrl.Manager) error {
	return InitClusterReconcilerWithLogger(ctrl.Log.WithName("controller").WithName("CrdbCluster"))
}

// InitClusterReconcilerWithLogger returns a registrator for new controller instance with provided logger
func InitClusterReconcilerWithLogger(l logr.Logger) func(ctrl.Manager) error {
	return func(mgr ctrl.Manager) error {
		clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
		if err != nil {
			return err
		}
		return (&ClusterReconciler{
			Client:   mgr.GetClient(),
			Log:      l,
			Scheme:   mgr.GetScheme(),
			Director: actor.NewDirector(mgr.GetScheme(), mgr.GetClient(), mgr.GetConfig(), clientset),
		}).SetupWithManager(mgr)
	}
}
