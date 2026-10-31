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
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/cenkalti/backoff"
	api "github.com/cockroachdb/cockroach-operator/apis/v1alpha1"
	"github.com/cockroachdb/cockroach-operator/pkg/kube"
	"github.com/cockroachdb/cockroach-operator/pkg/ptr"
	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/cockroachdb/errors"
	"github.com/go-logr/logr"
	"go.uber.org/zap/zapcore"
	kbatch "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	kubetypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newVersionChecker(scheme *runtime.Scheme, cl client.Client, clientset kubernetes.Interface) Actor {
	return &versionChecker{
		action: newAction(scheme, cl, nil, clientset),
	}
}

// versionChecker performs the validation of the crdb image for the new cluster
type versionChecker struct {
	action
}

// GetActionType returns api.VersionCheckerAction action used to set the cluster status errors
func (v *versionChecker) GetActionType() api.ActionType {
	return api.VersionCheckerAction
}

func (v *versionChecker) Act(ctx context.Context, cluster *resource.Cluster, log logr.Logger) error {
	log.V(DEBUGLEVEL).Info("starting to check the logging config provided")
	//we refresh the resource to make sure we use the latest version
	fetcher := resource.NewKubeFetcher(ctx, cluster.Namespace(), v.client)

	if cluster.IsLoggingAPIEnabled() {
		if logConfig, err := cluster.LoggingConfiguration(fetcher); err == nil {
			log.V(DEBUGLEVEL).Info(fmt.Sprintf("Log configuration for the cockroach cluster: %s", logConfig))
			var stderr bytes.Buffer
			cmd := exec.Command("bash", "-c", fmt.Sprintf("cockroach debug check-log-config --log=%s", logConfig))
			cmd.Stderr = &stderr
			cErr := cmd.Run()
			if cErr != nil || stderr.String() != "" {
				log.Error(cErr, "The cockroachdb logging API is set to value that is not supported by the operator, See the default logging configuration here (https://www.cockroachlabs.com/docs/stable/configure-logs.html#default-logging-configuration) ")
				return errors.New(stderr.String())
			} else {
				log.V(DEBUGLEVEL).Info("Validated the logging config")
			}
		} else {
			vErr := ValidationError{Err: err}
			log.Error(vErr, "The cockroachdb logging API value is set to a value that is not supported by the operator")
			return err
		}
	} else {
		log.V(DEBUGLEVEL).Info("logging API is not enabled")
	}

	log.V(DEBUGLEVEL).Info("starting to check the crdb version of the container provided")

	r := resource.NewManagedKubeResource(ctx, v.client, cluster, kube.RecreatingPersister)
	owner := cluster.Unwrap()

	// If the image.name is set use that value and do not check that the
	// version is set in the supported versions.
	// If it is not set then pass through the if statement and check that
	if cluster.Spec().Image == nil || cluster.Spec().Image.Name == "" {
		log.V(DEBUGLEVEL).Info("User set cockroachDBVersion")
		// we check if the cockroachDBVersion is supported by the operator,
		// this can return false only for api field CockroachDBVersion
		// The supported versions are set as enviroment variables in the operator manifest.
		if !cluster.IsSupportedImage() {
			err := ValidationError{Err: errors.New(fmt.Sprintf("crdb version %s not supported", cluster.Spec().CockroachDBVersion))}
			log.Error(err, "The cockroachDBVersion API value is set to a value that is not supported by the operator. Supported versions are set via the RELATED_IMAGE env variables in the operator manifest.")
			return err
		}
		log.V(int(zapcore.DebugLevel)).Info(fmt.Sprintf("supported CockroachDBVersion %s", cluster.Spec().CockroachDBVersion))
		return v.completeVersionChecker(ctx, cluster, cluster.Spec().CockroachDBVersion,
			cluster.GetCockroachDBImageName(), log)
	} else {
		log.V(int(zapcore.DebugLevel)).Info("User set image.name, using that field instead of cockroachDBVersion")
	}

	var calVersion, containerImage string
	//reset the values of the annotation and make sure we will have the correct one
	cluster.SetClusterVersion(calVersion)
	cluster.SetAnnotationVersion(calVersion)
	cluster.SetCrdbContainerImage(containerImage)
	cluster.SetAnnotationContainerImage(containerImage)
	jobName := cluster.JobName()
	_, err := (resource.Reconciler{
		ManagedResource: r,
		Builder: resource.JobBuilder{
			Cluster:  cluster,
			Selector: r.Labels.Selector(cluster.Spec().AdditionalLabels),
			JobName:  jobName,
		},
		Owner:  owner,
		Scheme: v.scheme,
	}).Reconcile()
	if err != nil {
		if kube.IgnoreNotFound(err) == nil {
			err := errors.Wrap(err, "failed to reconcile job not found")
			log.Error(err, "failed to reconcile job")
			return err
		}
		log.Error(err, "failed to reconcile job only error")
		return err
	}

	log.V(int(zapcore.DebugLevel)).Info("version checker", "job", jobName)
	key := kubetypes.NamespacedName{
		Namespace: cluster.Namespace(),
		Name:      jobName,
	}

	job := &kbatch.Job{}
	if err := v.client.Get(ctx, key, job); err != nil {
		log.Error(err, "failed getting Job '%s'", jobName)
		return err
	}

	// Left over insanity check just in case there's a missed edge case.
	// WaitUntilJobPodIsRunning will panic with a nil dereference if passed an
	// empty Job. There was previously an incorrect error check which would
	// always panic if the above .Get failed leading to some strange flakiness
	// in test. An extremely defensive block (See #607) was added as an attempt
	// to mitigate this panic (assumedly). It's been removed but this final
	// check is leftover just in case this after the fact correction was
	// misinformed.
	if job.Spec.Selector == nil {
		err := errors.New("job selector is nil")
		log.Error(err, err.Error())
		return err
	}

	// check if the job is completed or failed before EXEC
	if finished, _ := isJobCompletedOrFailed(job); !finished {
		if err := WaitUntilJobPodIsRunning(ctx, v.clientset, job, log); err != nil {
			// if after 2 minutes the job pod is not ready and container status is ImagePullBackoff
			// We need to stop requeueing until further changes on the CR
			image := cluster.GetCockroachDBImageName()
			if errBackoff := IsContainerStatusImagePullBackoff(ctx, v.clientset, job, log, image); errBackoff != nil {
				err := PermanentErr{Err: errBackoff}
				return LogError("job image incorrect", err, log)
			} else if dErr := deleteJob(ctx, cluster, v.clientset, job); dErr != nil {
				// Log the job deletion error, but return the underlying error that prompted deletion.
				log.Error(dErr, "failed to delete the job")
			}
			return errors.Wrapf(err, "failed to check the version of the crdb")
		}

		// Tail only last two lines as one line contains version info and one line has newline character. It is much faster.
		podLines := int64(2)
		podLogOpts := corev1.PodLogOptions{
			Container: resource.JobContainerName,
			TailLines: &podLines,
		}

		//get pod for the job we created
		pods, err := v.clientset.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labels.Set(job.Spec.Selector.MatchLabels).AsSelector().String(),
		})

		if err != nil {
			log.Error(err, "failed to find running pod for job")
			return errors.Wrapf(err, "failed to list running pod for job")
		}
		if len(pods.Items) == 0 {
			log.V(int(zapcore.DebugLevel)).Info("No running pods yet for version checker... we will retry later")
			return nil
		}
		tmpPod := &pods.Items[0]
		// when we have more jobs take the latest in consideration
		if len(pods.Items) > 1 {
			for _, po := range pods.Items {
				if !po.CreationTimestamp.Before(&tmpPod.CreationTimestamp) {
					tmpPod = &po
				}
			}
		}

		podName := tmpPod.Name

		req := v.clientset.CoreV1().Pods(job.Namespace).GetLogs(podName, &podLogOpts)
		podLogs, err := req.Stream(ctx)
		if err != nil {
			msg := "error in opening stream"
			log.Error(err, msg)
			return errors.Wrapf(err, msg)
		}
		defer podLogs.Close()

		buf := new(bytes.Buffer)
		_, err = io.Copy(buf, podLogs)
		if err != nil {
			msg := "error in copy information from podLogs to buf"
			log.Error(err, msg)
			return errors.Wrapf(err, msg)
		}
		output := buf.String()

		log.Info("Logs of version checker pod", "log", output)
		// This is the value from Build Tag taken from the container
		calVersion = strings.Replace(output, "\n", "", -1)
		// if no logs are found we will retry later to fetch the logs
		if calVersion == "" {
			err := NotReadyErr{Err: errors.New("failed to check the version of the cluster")}
			log.Error(err, "crdb version not found")
			return err
		}

		// If the user has not set image.name then check if the calVersion is supported
		// We already check above that if image.name is not set then cockroachDBVersion is set.
		if cluster.Spec().Image.Name == "" {
			// we check if the image tag version is supported by the operator
			if _, ok := cluster.LookupSupportedVersion(calVersion); !ok {
				err := ValidationError{Err: errors.New(fmt.Sprintf("crdb version %s not supported ", calVersion))}
				log.Error(err, "crdb version not supported")
				return err
			}
		}

		dbContainer, err := kube.FindContainer(resource.JobContainerName, &job.Spec.Template.Spec)
		if err != nil {
			log.Error(err, "unable to find container version")
			return err
		}
		containerImage = dbContainer.Image
		if strings.EqualFold(cluster.GetVersionAnnotation(), calVersion) {
			log.V(int(zapcore.DebugLevel)).Info("No update on version annotation -> nothing changed")
			return nil
		}
		if strings.EqualFold(cluster.GetAnnotationContainerImage(), containerImage) {
			log.V(int(zapcore.DebugLevel)).Info("No update on container image annotation -> nothing changed")
			return nil
		}
	} else {
		// after 2 minutes the pod enters in the completed state
		// if the container it is running and the version was not retrieved
		// for instance if the image is nginx and we want to get the crdb version from it case
		err := PermanentErr{Err: errors.New("job completed with version empty-container running but no version was retrieved")}
		log.Error(err, "job completed and we cannot find crdb version")
		return err
	}

	// If we got here, the version checker job was successful. Delete it.
	if dErr := deleteJob(ctx, cluster, v.clientset, job); dErr != nil {
		log.Error(dErr, "version checker job succeeded, but job failed to delete properly")
	}

	return v.completeVersionChecker(ctx, cluster, calVersion, containerImage, log)
}

// completeVersionChecker records the version the checker settled on. It is
// the single writer for both representations of that result, so neither way
// of selecting a version (spec.image.name or spec.cockroachDBVersion) can
// record one and forget the other:
//
//   - The crdb.io/version and crdb.io/containerimage annotations on the CR.
//     The StatefulSet builder copies these onto the StatefulSet, and
//     director.needsPartitionedUpdate compares the two copies to decide
//     whether a version change gets a partitioned update or a plain rollout.
//   - Status.Version, Status.CrdbContainerImage and the CrdbVersionChecked
//     condition, which the controller and users read.
//
// The annotation and status writes happen sequentially and are not atomic.
// Nothing reads the annotations until CrdbVersionChecked is true, so a
// failure in either write simply returns to the controller, which retries the
// version check. On the image.name path that retry runs another
// version-checker Job, because the previous Job is deleted before this
// function is called.
func (v *versionChecker) completeVersionChecker(
	ctx context.Context,
	cluster *resource.Cluster, version,
	imageName string,
	log logr.Logger) error {
	fetcher := resource.NewKubeFetcher(ctx, cluster.Namespace(), v.client)
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Re-fetch the CR so both writes below start from the current resourceVersion.
		cr := resource.ClusterPlaceholder(cluster.Name())
		if err := fetcher.Fetch(cr); err != nil {
			log.Error(err, "failed to retrieve CrdbCluster resource")
			return errors.Wrap(err, "failed to fetch CrdbCluster")
		}

		refreshedCluster := resource.NewCluster(cr)
		refreshedCluster.Fetcher = fetcher
		refreshedCluster.SetAnnotationVersion(version)
		refreshedCluster.SetAnnotationContainerImage(imageName)
		updated := refreshedCluster.Unwrap()
		if err := v.client.Update(ctx, updated); err != nil {
			log.Error(err, "failed saving the annotations on version checker")
			return errors.Wrap(err, "failed to save version annotations")
		}

		// Cluster.Unwrap returns a deep copy, so the resourceVersion that Update
		// wrote landed on updated, not on refreshedCluster. Re-wrapping updated
		// carries that resourceVersion into the status write below; without it
		// the status write would conflict with the annotation write.
		refreshedCluster = resource.NewCluster(updated)
		refreshedCluster.Fetcher = fetcher
		// save the status of the cluster
		refreshedCluster.SetTrue(api.CrdbVersionChecked)
		refreshedCluster.SetClusterVersion(version)
		refreshedCluster.SetCrdbContainerImage(imageName)
		if err := v.client.Status().Update(ctx, refreshedCluster.Unwrap()); err != nil {
			log.Error(err, "failed saving cluster status on version checker")
			return errors.Wrap(err, "failed to save version status")
		}
		return nil
	})
	if err != nil {
		return err
	}
	log.V(int(zapcore.DebugLevel)).Info("completed version checker", "calVersion", version,
		"containerImage", imageName)
	return nil
}

func isJobCompletedOrFailed(job *kbatch.Job) (bool, kbatch.JobConditionType) {
	for _, c := range job.Status.Conditions {
		if (c.Type == kbatch.JobComplete || c.Type == kbatch.JobFailed) && c.Status == corev1.ConditionTrue {
			return true, c.Type
		}
	}
	return false, ""
}

func deleteJob(ctx context.Context, cluster *resource.Cluster, clientset kubernetes.Interface, job *kbatch.Job) error {
	dp := metav1.DeletePropagationForeground
	return clientset.BatchV1().Jobs(cluster.Namespace()).Delete(ctx, job.Name, metav1.DeleteOptions{
		GracePeriodSeconds: ptr.Int64(5),
		PropagationPolicy:  &dp,
	})
}

func IsJobPodRunning(
	ctx context.Context,
	clientset kubernetes.Interface,
	job *kbatch.Job,
	l logr.Logger,
) error {
	//get pod for the job we created
	pods, err := clientset.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(job.Spec.Selector.MatchLabels).AsSelector().String(),
	})

	if err != nil {
		return LogError("error getting pod in job", err, l)
	}
	if len(pods.Items) == 0 {
		return LogError("job pods are not running yet waiting longer", nil, l)
	}
	pod := pods.Items[0]
	if !kube.IsPodReady(&pod) {
		pErr := fmt.Errorf("pod %s is not ready, and in %s phase", pod.Name, pod.Status.Phase)
		return LogError("job pod is not ready yet waiting longer", pErr, l)
	}
	l.V(int(zapcore.DebugLevel)).Info("job pod is ready")
	return nil
}

func IsContainerStatusImagePullBackoff(
	ctx context.Context,
	clientset kubernetes.Interface,
	job *kbatch.Job,
	l logr.Logger,
	image string,
) error {
	//get pod for the job we created
	pods, err := clientset.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(job.Spec.Selector.MatchLabels).AsSelector().String(),
	})
	//TO DO: maybe we should check some k8s specific errors here
	if err != nil {
		return LogError("error getting pod in job", err, l)
	}
	if len(pods.Items) == 0 {
		return LogError("job pods are not running.", nil, l)
	}
	pod := pods.Items[0]
	if !kube.IsPodReady(&pod) && kube.IsImagePullBackOff(&pod, image) {
		return LogError(fmt.Sprintf("Back-off pulling image %s", image), nil, l)
	}
	l.V(int(zapcore.DebugLevel)).Info("job pod is ready")
	return nil
}

func WaitUntilJobPodIsRunning(ctx context.Context, clientset kubernetes.Interface, job *kbatch.Job, l logr.Logger) error {
	if job == nil {
		return errors.New("job cannot be nil")
	}
	f := func() error {
		return IsJobPodRunning(ctx, clientset, job, l)
	}
	b := backoff.NewExponentialBackOff()
	b.MaxElapsedTime = 180 * time.Second
	b.MaxInterval = 10 * time.Second
	if err := backoff.Retry(f, b); err != nil {
		return errors.Wrapf(err, "pod is not running for job: %s", job.Name)
	}
	return nil
}

func LogError(msg string, err error, l logr.Logger) error {
	l.V(int(zapcore.DebugLevel)).Info(msg)
	if err == nil {
		return errors.New(msg)
	}
	return errors.Wrapf(err, msg)
}
