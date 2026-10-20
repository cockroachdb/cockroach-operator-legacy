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

package resource_test

import (
	"testing"

	featuregate "github.com/cockroachdb/cockroach-operator/pkg/featuregates"
	"github.com/cockroachdb/cockroach-operator/pkg/features"
	"github.com/cockroachdb/cockroach-operator/pkg/labels"
	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/cockroachdb/cockroach-operator/pkg/testutil"
	"github.com/cockroachdb/cockroach-operator/pkg/utilfeature"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	kbatch "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// schedulingTestCluster returns a cluster with every scheduling-related spec
// field set to a distinctive value, so a builder that drops or mis-copies one
// produces a visible difference.
func schedulingTestCluster(t *testing.T) *resource.Cluster {
	t.Helper()
	cr := testutil.NewBuilder("cockroachdb").
		Namespaced("default").
		WithUID("cockroachdb-uid").
		WithNodeCount(3).
		WithPVDataStore("1Gi").
		WithImage("cockroachdb/cockroach:v20.2.8").
		Cr()
	pullSecret := "registry-credentials"
	cr.Spec.Image.PullSecret = &pullSecret
	cr.Spec.NodeSelector = map[string]string{"workload": "cockroachdb"}
	cr.Spec.Tolerations = []corev1.Toleration{{
		Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "cockroachdb", Effect: corev1.TaintEffectNoSchedule,
	}}
	cr.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key: "kubernetes.io/arch", Operator: corev1.NodeSelectorOpIn, Values: []string{"arm64"},
			}},
		}}},
	}}
	cr.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
		MaxSkew: 1, TopologyKey: "topology.kubernetes.io/zone", WhenUnsatisfiable: corev1.DoNotSchedule,
		LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "cockroachdb"}},
	}}
	cr.Spec.PriorityClassName = "cockroachdb-critical"
	cluster := resource.NewCluster(cr)
	return &cluster
}

// withFeatureGates enables the given gates for this test and restores their
// previous values when it ends, so the process-global gate state does not
// leak into tests that run later in the package.
func withFeatureGates(t *testing.T, enabled map[featuregate.Feature]bool) {
	t.Helper()
	gate := utilfeature.DefaultMutableFeatureGate
	prev := map[string]bool{}
	want := map[string]bool{}
	for f, on := range enabled {
		prev[string(f)] = gate.Enabled(f)
		want[string(f)] = on
	}
	require.NoError(t, gate.SetFromMap(want))
	t.Cleanup(func() { require.NoError(t, gate.SetFromMap(prev)) })
}

// schedulingView strips the parts of a PodSpec that legitimately differ
// between the StatefulSet and the version-checker Job (what runs, how long
// it may take to stop, its service account token, its restart policy, its
// volumes), leaving everything that decides where the pod may run. Any
// scheduling field, present or future, therefore takes part in the
// comparison without being listed here.
func schedulingView(p corev1.PodSpec) corev1.PodSpec {
	p.Containers, p.InitContainers, p.Volumes = nil, nil, nil
	p.TerminationGracePeriodSeconds, p.AutomountServiceAccountToken = nil, nil
	p.RestartPolicy = ""
	return p
}

// TestBuildersApplyTheSameSchedulingFields builds the StatefulSet and the
// version-checker Job from one cluster and compares where their pods may
// run. Every pod the operator creates for a cluster must be schedulable
// where the database pods are, or an install or upgrade stalls on a Job
// that never lands.
//
// Both builders take these fields from Cluster.applySchedulingFields, so a
// field added to one cannot be forgotten by the other. This test compares
// everything in the pod spec that decides placement, so it also fails when a
// builder sets a new scheduling field itself and the two disagree.
func TestBuildersApplyTheSameSchedulingFields(t *testing.T) {
	withFeatureGates(t, map[featuregate.Feature]bool{
		features.AffinityRules: true, features.TolerationRules: true, features.TopologySpreadRules: true,
	})
	cluster := schedulingTestCluster(t)
	selector := labels.Common(cluster.Unwrap()).Selector(nil)

	sts := &appsv1.StatefulSet{}
	require.NoError(t, resource.StatefulSetBuilder{Cluster: cluster, Selector: selector, Telemetry: "test"}.Build(sts))
	job := &kbatch.Job{}
	require.NoError(t, resource.JobBuilder{Cluster: cluster, Selector: selector, JobName: "vcheck"}.Build(job))

	stsPod, jobPod := sts.Spec.Template.Spec, job.Spec.Template.Spec
	spec := cluster.Spec()

	// The StatefulSet carries everything the spec asked for.
	require.Equal(t, spec.Affinity, stsPod.Affinity)
	require.Equal(t, spec.Tolerations, stsPod.Tolerations)
	require.Equal(t, spec.TopologySpreadConstraints, stsPod.TopologySpreadConstraints)
	require.Equal(t, spec.NodeSelector, stsPod.NodeSelector)
	require.Equal(t, []corev1.LocalObjectReference{{Name: "registry-credentials"}}, stsPod.ImagePullSecrets)
	require.Equal(t, spec.PriorityClassName, stsPod.PriorityClassName)

	// The Job schedules exactly like the StatefulSet, nodeSelector included,
	// so it lands where the database pods do.
	require.Empty(t, cmp.Diff(schedulingView(stsPod), schedulingView(jobPod)))
}

// TestSchedulingFieldsHonorFeatureGates pins the default-off behavior of the
// three gated fields: with a gate off, neither builder copies that field,
// while the ungated fields are still applied. The gates default to off, so
// this is the path most clusters run.
func TestSchedulingFieldsHonorFeatureGates(t *testing.T) {
	cases := []struct {
		gate  featuregate.Feature
		field string
		get   func(corev1.PodSpec) any
	}{
		{features.AffinityRules, "Affinity", func(p corev1.PodSpec) any { return p.Affinity }},
		{features.TolerationRules, "Tolerations", func(p corev1.PodSpec) any { return p.Tolerations }},
		{features.TopologySpreadRules, "TopologySpreadConstraints", func(p corev1.PodSpec) any { return p.TopologySpreadConstraints }},
	}
	for _, tc := range cases {
		t.Run(string(tc.gate), func(t *testing.T) {
			gates := map[featuregate.Feature]bool{
				features.AffinityRules: true, features.TolerationRules: true, features.TopologySpreadRules: true,
			}
			gates[tc.gate] = false
			withFeatureGates(t, gates)
			cluster := schedulingTestCluster(t)
			selector := labels.Common(cluster.Unwrap()).Selector(nil)
			sts := &appsv1.StatefulSet{}
			require.NoError(t, resource.StatefulSetBuilder{Cluster: cluster, Selector: selector, Telemetry: "test"}.Build(sts))
			job := &kbatch.Job{}
			require.NoError(t, resource.JobBuilder{Cluster: cluster, Selector: selector, JobName: "vcheck"}.Build(job))
			for name, pod := range map[string]corev1.PodSpec{"statefulset": sts.Spec.Template.Spec, "job": job.Spec.Template.Spec} {
				require.Nil(t, tc.get(pod), "%s: %s must not be copied while its gate is off", name, tc.field)
				require.Equal(t, cluster.Spec().NodeSelector, pod.NodeSelector, "%s: ungated fields still apply", name)
				require.Equal(t, cluster.Spec().PriorityClassName, pod.PriorityClassName, "%s: ungated fields still apply", name)
			}
		})
	}
}
