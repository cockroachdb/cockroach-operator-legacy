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
	"errors"
	"fmt"

	"github.com/cockroachdb/cockroach-operator/pkg/kube"
	"github.com/cockroachdb/cockroach-operator/pkg/labels"
	"github.com/cockroachdb/cockroach-operator/pkg/ptr"
	kbatch "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// JobContainerName used on spec for the container
	JobContainerName     = "crdb"
	GetTagVersionCommand = "/cockroach/cockroach.sh version | grep 'Build Tag:'| awk '{print $3}'"
)

type JobBuilder struct {
	*Cluster

	Selector labels.Labels
	JobName  string
}

func (b JobBuilder) ResourceName() string {
	return b.JobName
}

func (b JobBuilder) Build(obj client.Object) error {
	job, ok := obj.(*kbatch.Job)
	if !ok {
		return errors.New("failed to cast to Job object")
	}

	if job.ObjectMeta.Name == "" {
		job.ObjectMeta.Name = b.JobName
	}

	job.Annotations = b.Spec().AdditionalAnnotations

	// we recreate spec from ground only if we do not find the container job
	if _, err := kube.FindContainer(JobContainerName, &job.Spec.Template.Spec); err != nil {
		backoffLimit := int32(2)
		job.Spec = kbatch.JobSpec{
			// This field is alpha-level and is only honored by servers that enable the TTLAfterFinished feature.
			// see https://v1-18.docs.kubernetes.io/docs/reference/generated/kubernetes-api/v1.18/#job-v1-batch
			TTLSecondsAfterFinished: ptr.Int32(300),
			Template:                b.buildPodTemplate(),
			BackoffLimit:            &backoffLimit,
		}
	}

	return nil
}
func (b JobBuilder) buildPodTemplate() corev1.PodTemplateSpec {
	pod := corev1.PodTemplateSpec{
		// per the docs you do not add a selector and you let the system
		// do this.
		// https://kubernetes.io/docs/concepts/workloads/controllers/job/#specifying-your-own-pod-selector
		/*
			ObjectMeta: metav1.ObjectMeta{
				Labels: b.Selector,
			},
		*/
		ObjectMeta: metav1.ObjectMeta{
			Annotations: b.Spec().AdditionalAnnotations,
		},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser: ptr.Int64(1000581000),
				FSGroup:   ptr.Int64(1000581000),
			},
			TerminationGracePeriodSeconds: ptr.Int64(60),
			Containers:                    b.MakeContainers(),
			AutomountServiceAccountToken:  ptr.Bool(false),
			ServiceAccountName:            b.ServiceAccountName(),
			RestartPolicy:                 corev1.RestartPolicyNever,
		},
	}

	b.applySchedulingFields(&pod.Spec)

	return pod
}

// MakeContainers creates a slice of corev1.Containers which includes a single
// corev1.Container that is based on the CR.
func (b JobBuilder) MakeContainers() []corev1.Container {
	// image := b.GetCockroachDBImageName()
	// TODO we need error handling to determine if we cannot find an image.
	// if image == NotSupportedVersion {
	//		panic("unable to find image")
	//}

	return []corev1.Container{
		{
			Name:            JobContainerName,
			Image:           b.GetCockroachDBImageName(),
			ImagePullPolicy: b.GetImagePullPolicy(),
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    apiresource.MustParse("300m"),
					corev1.ResourceMemory: apiresource.MustParse("512Mi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    apiresource.MustParse("300m"),
					corev1.ResourceMemory: apiresource.MustParse("256Mi"),
				},
			},
			Command: []string{"/bin/bash"},
			Args:    []string{"-c", fmt.Sprintf("set -eo pipefail; %s; sleep 150", GetTagVersionCommand)},
		},
	}
}
func (b JobBuilder) Placeholder() client.Object {
	return &kbatch.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: b.JobName,
		},
	}
}
