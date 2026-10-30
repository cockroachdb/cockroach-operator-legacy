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

package resource

import (
	"github.com/cockroachdb/cockroach-operator/pkg/features"
	"github.com/cockroachdb/cockroach-operator/pkg/utilfeature"
	corev1 "k8s.io/api/core/v1"
)

// applySchedulingFields applies the cluster's scheduling settings to a pod
// spec: affinity, tolerations and topology spread (each behind its feature
// gate), node selector, image pull secret and priority class. The database
// StatefulSet and the version-checker Job both go through it: a Job that
// cannot schedule where the database pods run never completes and stalls the
// install or upgrade. Builders must not set these fields themselves.
func (cluster Cluster) applySchedulingFields(podSpec *corev1.PodSpec) {
	spec := cluster.Spec()

	if utilfeature.DefaultMutableFeatureGate.Enabled(features.AffinityRules) {
		podSpec.Affinity = spec.Affinity
	}
	if utilfeature.DefaultMutableFeatureGate.Enabled(features.TolerationRules) {
		podSpec.Tolerations = spec.Tolerations
	}
	if utilfeature.DefaultMutableFeatureGate.Enabled(features.TopologySpreadRules) {
		podSpec.TopologySpreadConstraints = spec.TopologySpreadConstraints
	}
	if len(spec.NodeSelector) > 0 {
		podSpec.NodeSelector = spec.NodeSelector
	}
	if secret := cluster.GetImagePullSecret(); secret != nil {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: *secret}}
	}
	if spec.PriorityClassName != "" {
		podSpec.PriorityClassName = spec.PriorityClassName
	}
}
