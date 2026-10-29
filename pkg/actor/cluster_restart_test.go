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
	"github.com/go-logr/zapr"
	"go.uber.org/zap/zaptest"
	"testing"

	api "github.com/cockroachdb/cockroach-operator/apis/v1alpha1"
	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/cockroachdb/errors"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakeclient "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// HealthCheckerTes struct
type HealthCheckerTest struct{}

func (hc *HealthCheckerTest) Probe(ctx context.Context, l logr.Logger, logSuffix string, partition int) error {
	return nil
}

func TestClusterReadyForRestart(t *testing.T) {
	ssStatus := appsv1.StatefulSetStatus{
		Replicas:        5,
		CurrentReplicas: 5,
	}
	require.NoError(t, statefulSetReplicasAvailable(&ssStatus))

	ssStatus.CurrentReplicas = 3
	require.Error(t, statefulSetReplicasAvailable(&ssStatus))
}

func TestFullClusterRestart(t *testing.T) {
	// Setup fake client
	builder := fake.NewClientBuilder()

	client := builder.Build()

	cr := newClusterRestart(client, nil, nil).(*clusterRestart)
	require.NotNil(t, cr)
	stsReplicas := int32(3)
	cltSet := fakeclient.NewSimpleClientset()

	sts := createStatefulSet(stsReplicas)
	require.NoError(t, cltSet.Tracker().Add(&sts))

	require.NoError(t, addPodsToStatefulSet(stsReplicas, sts, cltSet))

	cltSet.PrependReactor("*", "*", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		tracker := cltSet.Tracker()
		gvr := action.GetResource()
		ns := action.GetNamespace()
		verb := action.GetVerb()

		switch verb {
		case "update":
			updateAction := action.(clienttesting.UpdateAction)
			obj := updateAction.GetObject().(*appsv1.StatefulSet)
			if err := tracker.Update(gvr, obj, ns); err != nil {
				return false, nil, err
			}
			return true, obj, nil
		case "get":
			getAction := action.(clienttesting.GetAction)
			obj, err := tracker.Get(gvr, ns, getAction.GetName())
			if err != nil {
				return false, nil, err
			}
			return true, obj, nil
		}

		return false, nil, nil
	})

	sts.Status.Replicas = stsReplicas
	sts.Status.ReadyReplicas = stsReplicas

	err := cltSet.Tracker().Update(schema.GroupVersionResource{
		Group:    "apps",
		Version:  "v1",
		Resource: "statefulsets",
	}, &sts, sts.Namespace)
	require.NoError(t, err)
	testLog := zapr.NewLogger(zaptest.NewLogger(t))
	require.NoError(t, cr.fullClusterRestart(context.TODO(), &sts, testLog, cltSet))
}

func TestRollingClusterRestart(t *testing.T) {
	stsReplicas := int32(3)
	cltSet := fakeclient.NewSimpleClientset()

	sts := createStatefulSet(stsReplicas)
	require.NoError(t, cltSet.Tracker().Add(&sts))

	require.NoError(t, addPodsToStatefulSet(stsReplicas, sts, cltSet))

	cltSet.PrependReactor("*", "*", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		tracker := cltSet.Tracker()
		gvr := action.GetResource()
		ns := action.GetNamespace()
		verb := action.GetVerb()

		switch verb {
		case "update":
			updateAction := action.(clienttesting.UpdateAction)
			obj := updateAction.GetObject().(*appsv1.StatefulSet)
			if err := tracker.Update(gvr, obj, ns); err != nil {
				return false, nil, err

			}
			return true, obj, nil
		case "get":
			getAction := action.(clienttesting.GetAction)
			obj, err := tracker.Get(gvr, ns, getAction.GetName())
			if err != nil {
				return false, nil, err
			}
			return true, obj, nil
		}

		return false, nil, nil
	})

	sts.Status.Replicas = stsReplicas
	sts.Status.ReadyReplicas = stsReplicas

	err := cltSet.Tracker().Update(schema.GroupVersionResource{
		Group:    "apps",
		Version:  "v1",
		Resource: "statefulsets",
	}, &sts, sts.Namespace)
	require.NoError(t, err)

	hcTest := HealthCheckerTest{}
	testLog := zapr.NewLogger(zaptest.NewLogger(t))

	// Setup fake client
	builder := fake.NewClientBuilder()
	client := builder.Build()
	cr := newClusterRestart(client, nil, cltSet).(*clusterRestart)
	require.NotNil(t, cr)

	require.NoError(t, cr.rollingSts(context.TODO(), &sts, testLog, &hcTest))
}

func createStatefulSet(stsReplicas int32) appsv1.StatefulSet {
	return appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "statefulset",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Annotations: make(map[string]string),
			Name:        "crdb-sts",
			Namespace:   "crdb",
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/component": "crdb-sts",
					"app.kubernetes.io/instance":  "crdb-sts",
					"app.kubernetes.io/name":      "crdb-sts",
				},
				MatchExpressions: nil,
			},
			Replicas: &stsReplicas,
		},
	}
}

func addPodsToStatefulSet(stsReplicas int32, sts appsv1.StatefulSet, cltSet *fakeclient.Clientset) error {
	// Create some pods to look up
	var i int32
	for i = 0; i < stsReplicas; i++ {
		pod := corev1.Pod{
			TypeMeta: metav1.TypeMeta{
				Kind:       "pod",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%v", sts.Name, i),
				Namespace: sts.Namespace,
				Labels:    sts.Spec.Selector.MatchLabels,
			},
			Status: corev1.PodStatus{
				Phase: "Running",
			},
		}

		if err := cltSet.Tracker().Add(&pod); err != nil {
			return err
		}
	}
	return nil
}

func TestClusterRestartAnnotationCleanupError(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))

	cr := &api.CrdbCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "crdb",
			Namespace:   "crdb",
			Annotations: map[string]string{resource.CrdbRestartTypeAnnotation: "Rolling"},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(context.Context, crclient.WithWatch, crclient.Object, ...crclient.UpdateOption) error {
				return errors.New("boom")
			},
		}).Build()

	r := newClusterRestart(client, nil, nil).(*clusterRestart)
	cluster := resource.NewCluster(cr)

	// The annotation is what selects the restart action, so a failed cleanup has
	// to surface or the cluster restarts again on the next reconcile.
	err := r.clearRestartTypeAnnotation(context.TODO(), &cluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.EqualError(t, err, "boom")
}

func TestClusterRestartAnnotationCleanupRetriesConflict(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))

	cr := &api.CrdbCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "crdb",
			Namespace:   "crdb",
			Annotations: map[string]string{resource.CrdbRestartTypeAnnotation: "Rolling"},
		},
	}

	var updates int
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c crclient.WithWatch, obj crclient.Object, opts ...crclient.UpdateOption) error {
				updates++
				if updates == 1 {
					return k8sErrors.NewConflict(
						schema.GroupResource{Resource: "crdbclusters"}, obj.GetName(), errors.New("conflict"))
				}
				return c.Update(ctx, obj, opts...)
			},
		}).Build()

	r := newClusterRestart(client, nil, nil).(*clusterRestart)
	cluster := resource.NewCluster(cr)

	// A conflict must be retried here. Leaving the annotation behind would make
	// the next reconcile roll the cluster again.
	require.NoError(t, r.clearRestartTypeAnnotation(context.TODO(), &cluster, zapr.NewLogger(zaptest.NewLogger(t))))
	require.Equal(t, 2, updates)

	updated := &api.CrdbCluster{}
	require.NoError(t, client.Get(context.TODO(), crclient.ObjectKeyFromObject(cr), updated))
	require.NotContains(t, updated.Annotations, resource.CrdbRestartTypeAnnotation)
}
