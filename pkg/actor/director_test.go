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

package actor_test

import (
	"context"
	"slices"
	"testing"

	api "github.com/cockroachdb/cockroach-operator/apis/v1alpha1"
	"github.com/cockroachdb/cockroach-operator/pkg/actor"
	"github.com/cockroachdb/cockroach-operator/pkg/kube"
	"github.com/cockroachdb/cockroach-operator/pkg/labels"
	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/cockroachdb/cockroach-operator/pkg/testutil"
	"github.com/go-logr/zapr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// This constructs a mock cluster that behaves as if it were a real cluster in a steady state.
// Kept for existing callers; see newTestDirector for the configurable form.
func createTestDirectorAndStableCluster(t *testing.T) (*resource.Cluster, actor.Director, *fake.Clientset) {
	cluster, director, _, clientset := newTestDirector(t, testDirectorOptions{})
	return cluster, director, clientset
}

// testDirectorOptions describe how a mock steady-state cluster differs from
// the default one built by createTestDirectorAndStableCluster.
type testDirectorOptions struct {
	// builder runs on the cluster builder before .Cluster() is called. It is
	// the only hook that can set spec fields: resource.Cluster exposes no spec
	// setters (Spec() returns a copy). It runs after the defaults, so it can
	// override them, e.g. WithClusterAnnotations replaces the default map.
	builder func(testutil.ClusterBuilder) testutil.ClusterBuilder
	// mutateCluster runs after the builder produces the cluster and before any
	// component is built from it. Use it for status: conditions and
	// annotations.
	mutateCluster func(*resource.Cluster)
	// statefulSetAnnotations are applied to the StatefulSet after CompleteBuild
	// regenerates it from the cluster, so the StatefulSet can look like one
	// deployed at a different version than the cluster now asks for. nil keeps
	// the version the builder copied from the cluster.
	statefulSetAnnotations map[string]string
	// seedClusterInClient stores the CrdbCluster itself in the
	// controller-runtime fake client. Actors that fetch and update the CR
	// (the version checker) need this; the director predicates do not.
	seedClusterInClient bool
}

// newTestDirector builds a mock steady-state cluster, its components, and a
// director wired to fake clients. The returned FakeClient is the
// controller-runtime client the director uses.
func newTestDirector(
	t *testing.T, opts testDirectorOptions,
) (*resource.Cluster, actor.Director, *testutil.FakeClient, *fake.Clientset) {
	var numNodes int32 = 4
	version := "fake.version"
	storage := "1Gi"

	// Initialize a mock cluster with various attributes
	clusterAnnotations := map[string]string{
		resource.CrdbVersionAnnotation: version,
	}
	cb := testutil.NewBuilder("cockroachdb").
		Namespaced("default").
		WithUID("cockroachdb-uid").
		WithPVDataStore(storage).
		WithNodeCount(numNodes).
		WithClusterAnnotations(clusterAnnotations)
	if opts.builder != nil {
		cb = opts.builder(cb)
	}
	cluster := cb.Cluster()
	// A stable cluster has a checked version and is initialized
	cluster.SetTrue(api.CrdbVersionChecked)
	cluster.SetTrue(api.CrdbInitializedCondition)
	if opts.mutateCluster != nil {
		opts.mutateCluster(cluster)
	}

	// Mock node for our mock cluster
	node := &v1.Node{}
	serviceAccount := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cockroachdb-sa",
			Namespace: "default",
		},
	}
	objs := []runtime.Object{
		node,
		serviceAccount,
	}

	// Mock components of our mock cluster
	discoveryService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cockroachdb",
			Namespace: "default",
		},
	}
	publicService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cockroachdb-public",
			Namespace: "default",
		},
	}
	quantity, _ := apiresource.ParseQuantity(storage)
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cockroachdb",
			Namespace: "default",
			Annotations: map[string]string{
				resource.CrdbVersionAnnotation: version,
			},
		},
		Status: appsv1.StatefulSetStatus{
			Replicas:        numNodes,
			CurrentReplicas: numNodes,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &numNodes,
			VolumeClaimTemplates: []v1.PersistentVolumeClaim{
				{
					Spec: v1.PersistentVolumeClaimSpec{
						Resources: v1.VolumeResourceRequirements{
							Requests: v1.ResourceList{
								v1.ResourceStorage: quantity,
							},
						},
					},
				},
			},
		},
	}
	podDisruptionBudget := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cockroachdb",
			Namespace: "default",
		},
	}
	components := []client.Object{
		discoveryService,
		publicService,
		statefulSet,
		podDisruptionBudget,
	}

	// Build up all the required pieces of our cluster components
	l := labels.Common(cluster.Unwrap())
	kd, _ := kube.NewKubernetesDistribution().Get(context.Background(), fake.NewSimpleClientset(objs...), zapr.NewLogger(zaptest.NewLogger(t)))
	scheme := testutil.InitScheme(t)
	builders := []resource.Builder{
		resource.DiscoveryServiceBuilder{Cluster: cluster, Selector: l.Selector(nil)},
		resource.PublicServiceBuilder{Cluster: cluster, Selector: l.Selector(nil)},
		resource.StatefulSetBuilder{Cluster: cluster, Selector: l.Selector(nil), Telemetry: kd},
		resource.PdbBuilder{Cluster: cluster, Selector: l.Selector(nil)},
	}
	for i := range builders {
		require.NoError(t, resource.Reconciler{
			ManagedResource: resource.ManagedResource{Labels: l},
			Builder:         builders[i],
			Owner:           cluster.Unwrap(),
			Scheme:          scheme,
		}.CompleteBuild(components[i].DeepCopyObject(), components[i]))
		objs = append(objs, components[i])
	}

	// CompleteBuild rebuilt the StatefulSet from the cluster; apply any
	// requested annotations on top so it can look like an older deployment.
	for k, v := range opts.statefulSetAnnotations {
		statefulSet.Annotations[k] = v
	}

	// Construct mock cluster access. The typed clientset does not know the
	// CrdbCluster type (fake.NewSimpleClientset panics on unregistered kinds),
	// so the cluster goes only into the controller-runtime client.
	clientObjs := slices.Clone(objs)
	if opts.seedClusterInClient {
		clientObjs = append(clientObjs, cluster.Unwrap())
	}
	cl := testutil.NewFakeClient(scheme, clientObjs...)
	clientset := fake.NewSimpleClientset(objs...)
	config := &rest.Config{}
	director := actor.NewDirector(scheme, cl, config, clientset)

	return cluster, director, cl, clientset
}

func TestNoActionRequired(t *testing.T) {
	cluster, director, _ := createTestDirectorAndStableCluster(t)

	// We made no changes to the steady-state mock cluster. No actor should trigger.
	actor, err := director.GetActorToExecute(context.Background(), cluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Nil(t, actor)
}

func TestNeedsRestart(t *testing.T) {
	cluster, director, _ := createTestDirectorAndStableCluster(t)
	updated := cluster.Unwrap()

	// Trigger restart by adding restart annotation
	updated.Annotations = make(map[string]string)
	updated.Annotations[resource.CrdbRestartTypeAnnotation] = "Rolling"

	newCluster := resource.NewCluster(updated)
	actor, err := director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.ClusterRestartAction, actor.GetActionType())

	// Make a change that disables this actor, and check that it's no longer triggered
	newCluster.SetFalse(api.CrdbVersionChecked)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.VersionCheckerAction, actor.GetActionType())
}

func TestNeedsRBACSetup(t *testing.T) {
	cluster, director, clientset := createTestDirectorAndStableCluster(t)

	// Trigger RBAC setup by deleting service account
	serviceAccounts := clientset.CoreV1().ServiceAccounts(cluster.Namespace())
	err := serviceAccounts.Delete(context.Background(), cluster.ServiceAccountName(), metav1.DeleteOptions{})
	require.Nil(t, err)

	actor, err := director.GetActorToExecute(context.Background(), cluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.SetupRBACAction, actor.GetActionType())
}

func TestNeedsDecommission(t *testing.T) {
	cluster, director, _ := createTestDirectorAndStableCluster(t)
	updated := cluster.Unwrap()

	// Trigger decommission by decreasing nodes
	updated.Spec.Nodes = 3

	newCluster := resource.NewCluster(updated)
	actor, err := director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.DecommissionAction, actor.GetActionType())

	// Make a change that disables this actor, and check that it's no longer triggered
	newCluster.SetFalse(api.CrdbInitializedCondition)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.DeployAction, actor.GetActionType())
}

func TestNeedsVersionCheck(t *testing.T) {
	cluster, director, _ := createTestDirectorAndStableCluster(t)

	// Trigger version check by setting condition to false
	cluster.SetFalse(api.CrdbVersionChecked)

	actor, err := director.GetActorToExecute(context.Background(), cluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.VersionCheckerAction, actor.GetActionType())

	// Make a change that disables this actor, and check that it's no longer triggered
	cluster.SetTrue(api.CrdbVersionChecked)
	actor, err = director.GetActorToExecute(context.Background(), cluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Nil(t, actor)
}

func TestNeedsCertificate(t *testing.T) {
	cluster, director, _ := createTestDirectorAndStableCluster(t)
	updated := cluster.Unwrap()

	// Trigger certificate generation by enabling TLS
	updated.Spec.TLSEnabled = true

	newCluster := resource.NewCluster(updated)
	actor, err := director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.GenerateCertAction, actor.GetActionType())

	// Make a change that disables this actor, and check that it's no longer triggered
	updated.Spec.NodeTLSSecret = "soylent.green.is.people"
	newCluster = resource.NewCluster(updated)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.DeployAction, actor.GetActionType())
}

func TestNeedsUpdate(t *testing.T) {
	cluster, director, _ := createTestDirectorAndStableCluster(t)
	updated := cluster.Unwrap()

	// Trigger update by changing requested version
	updated.Annotations = make(map[string]string)
	updated.Annotations[resource.CrdbVersionAnnotation] = "fake.version.2"

	newCluster := resource.NewCluster(updated)
	actor, err := director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.PartitionedUpdateAction, actor.GetActionType())

	// Make a change that disables this actor, and check that it's no longer triggered
	newCluster.SetFalse(api.CrdbInitializedCondition)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.DeployAction, actor.GetActionType())
}

func TestNeedsPVCResize(t *testing.T) {
	cluster, director, _ := createTestDirectorAndStableCluster(t)
	updated := cluster.Unwrap()

	// Trigger PVC resize by increasing requested amount
	quantity, _ := apiresource.ParseQuantity("2Gi")
	updated.Spec.DataStore.VolumeClaim.PersistentVolumeClaimSpec.Resources.Requests[v1.ResourceStorage] = quantity

	newCluster := resource.NewCluster(updated)
	actor, err := director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.ResizePVCAction, actor.GetActionType())

	// Make a change that disables this actor, and check that it's no longer triggered
	newCluster.SetFalse(api.CrdbInitializedCondition)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.DeployAction, actor.GetActionType())
}

func TestNeedsDeploy(t *testing.T) {
	cluster, director, _ := createTestDirectorAndStableCluster(t)
	updated := cluster.Unwrap()

	// Trigger deploy by increasing nodes
	updated.Spec.Nodes = 5

	newCluster := resource.NewCluster(updated)
	actor, err := director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.DeployAction, actor.GetActionType())

	// Make a change that disables this actor, and check that it's no longer triggered
	newCluster.SetFalse(api.CrdbVersionChecked)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.VersionCheckerAction, actor.GetActionType())
}

func TestNeedsInitialization(t *testing.T) {
	cluster, director, _ := createTestDirectorAndStableCluster(t)

	// Trigger initialization by setting the condition to false
	cluster.SetFalse(api.CrdbInitializedCondition)

	actor, err := director.GetActorToExecute(context.Background(), cluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.InitializeAction, actor.GetActionType())

	// Make a change that disables this actor, and check that it's no longer triggered
	cluster.SetFalse(api.CrdbVersionChecked)
	actor, err = director.GetActorToExecute(context.Background(), cluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.VersionCheckerAction, actor.GetActionType())
}

func TestNeedsIngress(t *testing.T) {
	cluster, director, _ := createTestDirectorAndStableCluster(t)
	updated := cluster.Unwrap()

	// Trigger expose ingress by adding ingressConfig
	updated.Spec.Ingress = &api.IngressConfig{
		UI: &api.Ingress{
			IngressClassName: "test-class",
			Annotations:      map[string]string{"key": "value"},
			TLS:              nil,
			Host:             "ui.test.com",
		}}

	newCluster := resource.NewCluster(updated)
	actor, err := director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.ExposeIngressAction, actor.GetActionType())

	// Make a change that disables this actor, and check that it's no longer triggered
	newCluster.SetFalse(api.CrdbInitializedCondition)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.InitializeAction, actor.GetActionType())
}

// Make successive changes to the cluster and check that each change triggers an actor earlier in the order
func TestOrderOfActors(t *testing.T) {
	cluster, director, clientset := createTestDirectorAndStableCluster(t)

	// We made no changes to the steady-state mock cluster. No actor should trigger.
	actor, err := director.GetActorToExecute(context.Background(), cluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, nil, actor)

	// Trigger expose ingress by adding ingressConfig
	updated := cluster.Unwrap()
	updated.Spec.Ingress = &api.IngressConfig{
		UI: &api.Ingress{
			IngressClassName: "test-class",
			Annotations:      map[string]string{"key": "value"},
			TLS:              nil,
			Host:             "ui.test.com",
		}}

	newCluster := resource.NewCluster(updated)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.ExposeIngressAction, actor.GetActionType())

	// Trigger initialization by setting the condition to false
	cluster.SetFalse(api.CrdbInitializedCondition)
	actor, err = director.GetActorToExecute(context.Background(), cluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.InitializeAction, actor.GetActionType())

	// Trigger deploy by increasing nodes
	updated = cluster.Unwrap()
	updated.Spec.Nodes = 5
	newCluster = resource.NewCluster(updated)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.DeployAction, actor.GetActionType())

	// Trigger PVC resize by increasing requested amount
	newCluster.SetTrue(api.CrdbInitializedCondition)
	updated = newCluster.Unwrap()
	quantity, _ := apiresource.ParseQuantity("2Gi")
	updated.Spec.DataStore.VolumeClaim.PersistentVolumeClaimSpec.Resources.Requests[v1.ResourceStorage] = quantity
	newCluster = resource.NewCluster(updated)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.ResizePVCAction, actor.GetActionType())

	// Trigger update by changing requested version
	updated = newCluster.Unwrap()
	updated.Annotations = make(map[string]string)
	updated.Annotations[resource.CrdbVersionAnnotation] = "fake.version.2"
	newCluster = resource.NewCluster(updated)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.PartitionedUpdateAction, actor.GetActionType())

	// Trigger certificate generation by enabling TLS
	updated = newCluster.Unwrap()
	updated.Spec.TLSEnabled = true
	newCluster = resource.NewCluster(updated)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.GenerateCertAction, actor.GetActionType())

	// Trigger version check by setting condition to false
	newCluster.SetFalse(api.CrdbVersionChecked)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.VersionCheckerAction, actor.GetActionType())

	// Trigger decommission by decreasing nodes
	updated = newCluster.Unwrap()
	updated.Spec.Nodes = 3
	newCluster = resource.NewCluster(updated)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.DecommissionAction, actor.GetActionType())

	// Trigger RBAC setup by deleting service account
	serviceAccounts := clientset.CoreV1().ServiceAccounts(cluster.Namespace())
	err = serviceAccounts.Delete(context.Background(), cluster.ServiceAccountName(), metav1.DeleteOptions{})
	require.Nil(t, err)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.SetupRBACAction, actor.GetActionType())

	// Trigger restart by adding restart annotation
	newCluster.SetTrue(api.CrdbVersionChecked)
	updated = newCluster.Unwrap()
	updated.Annotations = make(map[string]string)
	updated.Annotations[resource.CrdbRestartTypeAnnotation] = "Rolling"
	newCluster = resource.NewCluster(updated)
	actor, err = director.GetActorToExecute(context.Background(), &newCluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.Nil(t, err)
	require.Equal(t, api.ClusterRestartAction, actor.GetActionType())
}
