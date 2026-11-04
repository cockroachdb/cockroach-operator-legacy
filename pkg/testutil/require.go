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

package testutil

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	api "github.com/cockroachdb/cockroach-operator/apis/v1alpha1"
	"github.com/cockroachdb/cockroach-operator/pkg/database"
	"github.com/cockroachdb/cockroach-operator/pkg/kube"
	"github.com/cockroachdb/cockroach-operator/pkg/labels"
	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	testenv "github.com/cockroachdb/cockroach-operator/pkg/testutil/env"
	"github.com/cockroachdb/errors"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// RequireClusterToBeReadyEventuallyTimeout tests to see if a statefulset has started correctly and
// all of the pods are ready.
func RequireClusterToBeReadyEventuallyTimeout(t *testing.T, sb testenv.DiffingSandbox, b ClusterBuilder, timeout time.Duration) {
	ctxWithTimeout, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cluster := b.Cluster()

	require.NoError(t, wait.PollUntilContextCancel(ctxWithTimeout, 10*time.Second, true, func(ctxWithTimeout context.Context) (bool, error) {
		ss, err := fetchStatefulSet(sb, cluster.StatefulSetName())
		if err != nil {
			t.Logf("error fetching stateful set")
			return false, err
		}

		if ss == nil {
			t.Logf("stateful set is not found")
			return false, nil
		}

		if !statefulSetIsReady(ss) {
			t.Logf("stateful set is not ready")
			if err = logPods(context.TODO(), ss, cluster, sb, t); err != nil {
				t.Log(err.Error())
			}
			return false, nil
		}

		return true, nil
	}))

	t.Log("Cluster is ready")
}

func RequireAtMostOneVersionCheckerJob(t *testing.T, sb testenv.DiffingSandbox, timeout time.Duration) {
	numTimesVersionCheckerSeen := 0
	ctx := context.Background()
	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		jobs, err := fetchJobs(sb)
		if err != nil {
			return false, errors.Newf("error fetching jobs: %v", err)
		}

		numVersionCheckerJobs := 0
		for _, job := range jobs {
			if strings.Contains(job.Name, resource.VersionCheckJobName) {
				numVersionCheckerJobs++
			}
		}
		if numVersionCheckerJobs > 1 {
			return false, errors.New("too many version checker jobs")
		} else if numVersionCheckerJobs == 1 {
			numTimesVersionCheckerSeen++
		}

		return false, nil
	})

	// Require that there was never more than one version checker job, and that the version checker job was in fact observed.
	require.Greater(t, numTimesVersionCheckerSeen, 0)
	require.True(t, wait.Interrupted(err), "expected polling to time out, got: %v", err)
}

func fetchJobs(sb testenv.DiffingSandbox) ([]batchv1.Job, error) {
	var jobs batchv1.JobList
	if err := sb.List(&jobs, nil); err != nil {
		return nil, err
	}
	return jobs.Items, nil
}

// TODO are we using this??

func RequireClusterToBeReadyEventually(t *testing.T, sb testenv.DiffingSandbox, b ClusterBuilder) {
	ctx := context.Background()
	cluster := b.Cluster()

	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, 60*time.Second, true, func(ctx context.Context) (bool, error) {

		ss, err := fetchStatefulSet(sb, cluster.StatefulSetName())
		if err != nil {
			t.Logf("error fetching stateful set")
			return false, err
		}

		if ss == nil {
			t.Logf("stateful set is not found")
			return false, nil
		}
		if !statefulSetIsReady(ss) {
			t.Logf("stateful set is not ready")
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err)
}

// RequireDbContainersToUseImage checks that the database is using the correct image
func RequireDbContainersToUseImage(t *testing.T, sb testenv.DiffingSandbox, cr *api.CrdbCluster) {
	ctx := context.Background()
	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, 600*time.Second, true, func(ctx context.Context) (bool, error) {
		pods, err := fetchPodsInStatefulSet(sb, labels.Common(cr).Selector(cr.Spec.AdditionalLabels))
		if err != nil {
			return false, err
		}

		if len(pods) < int(cr.Spec.Nodes) {
			return false, nil
		}

		res := testPodsWithPredicate(pods, func(p *corev1.Pod) bool {
			c, err := kube.FindContainer(resource.DbContainerName, &p.Spec)
			if err != nil {
				return false
			}
			if cr.Spec.Image.Name == "" {
				version := strings.ReplaceAll(cr.Spec.CockroachDBVersion, ".", "_")
				image := os.Getenv(fmt.Sprintf("RELATED_IMAGE_COCKROACH_%s", version))
				return c.Image == image
			}
			return c.Image == cr.Spec.Image.Name
		})

		return res, nil
	})

	require.NoError(t, err)
}

func fetchStatefulSet(sb testenv.DiffingSandbox, name string) (*appsv1.StatefulSet, error) {
	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	if err := sb.Get(ss); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}

		return nil, err
	}

	return ss, nil
}

func fetchPodsInStatefulSet(sb testenv.DiffingSandbox, labels map[string]string) ([]corev1.Pod, error) {
	var pods corev1.PodList

	if err := sb.List(&pods, labels); err != nil {
		return nil, err
	}

	return pods.Items, nil
}

func testPodsWithPredicate(pods []corev1.Pod, pred func(*corev1.Pod) bool) bool {
	for i := range pods {
		if !pred(&pods[i]) {
			return false
		}
	}

	return true
}

func statefulSetIsReady(ss *appsv1.StatefulSet) bool {
	return ss.Status.ReadyReplicas == *ss.Spec.Replicas
}

// TODO we are not using this

func RequireDownGradeOptionSet(t *testing.T, sb testenv.DiffingSandbox, b ClusterBuilder, version string) {
	sb.Mgr.GetConfig()
	podName := fmt.Sprintf("%s-0.%s", b.Cluster().Name(), b.Cluster().Name())
	conn := &database.DBConnection{
		Ctx:    context.TODO(),
		Client: sb.Mgr.GetClient(),
		Port:   b.Cluster().Spec().SQLPort,
		UseSSL: true,

		RestConfig:   sb.Mgr.GetConfig(),
		ServiceName:  podName,
		Namespace:    sb.Namespace,
		DatabaseName: "system",

		RunningInsideK8s:            false,
		ClientCertificateSecretName: b.Cluster().ClientTLSSecretName("root"),
		RootCertificateSecretName:   b.Cluster().NodeTLSSecretName(),
	}

	// Create a new database connection for the update.
	db, err := database.NewDbConnection(conn)
	require.NoError(t, err)
	defer db.Close()

	r := db.QueryRowContext(context.TODO(), "SHOW CLUSTER SETTING cluster.preserve_downgrade_option")
	var value string
	if err := r.Scan(&value); err != nil {
		t.Fatal(err)
	}

	if value == "" {
		t.Errorf("downgrade_option is empty and should be set to %s", version)
	}
}

// TODO I do not think this is correct.  Keith mentioned we need to check something else.

// RequireDecommissionNode requires that proper nodes are decommissioned
func RequireDecommissionNode(t *testing.T, sb testenv.DiffingSandbox, b ClusterBuilder, numNodes int32) {
	ctx := context.Background()
	cluster := b.Cluster()

	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, 700*time.Second, true, func(ctx context.Context) (bool, error) {
		sts, err := fetchStatefulSet(sb, cluster.StatefulSetName())
		if err != nil {
			t.Logf("statefulset is not found %v", err)
			return false, err
		}

		if sts == nil {
			t.Log("statefulset is not found")
			return false, nil
		}

		if !statefulSetIsReady(sts) {
			t.Log("statefulset is not ready")
			return false, nil
		}

		if numNodes != sts.Status.Replicas {
			t.Log("statefulset replicas do not match")
			return false, nil
		}
		//
		err = makeDrainStatusChecker(t, sb, b, uint64(numNodes))
		if err != nil {
			t.Logf("makeDrainStatusChecker failed due to error %v\n", err)
			return false, nil
		}
		return true, nil
	})

	require.NoError(t, err)
	t.Log("Done decommissioning node")
}

func makeDrainStatusChecker(t *testing.T, sb testenv.DiffingSandbox, b ClusterBuilder, numNodes uint64) error {
	cluster := b.Cluster()
	cmd := []string{"/cockroach/cockroach", "node", "status", "--decommission", "--format=csv", cluster.SecureMode()}
	podname := fmt.Sprintf("%s-0", cluster.StatefulSetName())
	stdout, stderror, err := kube.ExecInPod(context.TODO(), sb.Mgr.GetScheme(), sb.Mgr.GetConfig(), sb.Namespace,
		podname, resource.DbContainerName, cmd)
	if err != nil || stderror != "" {
		t.Logf("exec cmd = %s on pod=%s exit with error %v and stdError %s and ns %s", cmd, podname, err, stderror, sb.Namespace)
		return err
	}
	r := csv.NewReader(strings.NewReader(stdout))
	// skip header
	if _, err := r.Read(); err != nil {
		return err
	}
	// We are using the host to filter the decommissioned node.
	// Currently the id does not match the pod index because of the
	// pod parallel strategy
	host := fmt.Sprintf("%s-%d.%s.%s", cluster.StatefulSetName(),
		numNodes, cluster.StatefulSetName(), sb.Namespace)
	for {
		record, err := r.Read()
		if err == io.EOF {
			t.Log("Done reading node statuses")
			break
		}

		if err != nil {
			return errors.Wrapf(err, "failed to get node draining status")
		}

		idStr, address := record[0], record[1]

		if !strings.Contains(address, host) {
			continue
		}
		//if the address is for the last pod that was decommissioned we are checking the replicas
		id, err := strconv.ParseUint(idStr, 10, 32)
		if err != nil {
			return errors.Wrap(err, "failed to extract node id from string")
		}

		isLive, replicasStr, isDecommissioning := record[8], record[9], record[10]
		t.Logf("draining node do to decommission test\n")
		t.Logf("id=%s\n ", idStr)
		t.Logf("address=%s\n ", address)
		t.Logf("isLive=%s\n ", isLive)
		t.Logf("replicas=%s\n", replicasStr)
		t.Logf("isDecommissioning=%v\n", isDecommissioning)

		if isLive != "false" {
			return errors.New("unexpected node status")
		}

		replicas, err := strconv.ParseUint(replicasStr, 10, 64)
		if err != nil {
			return errors.Wrap(err, "failed to parse replicas number")
		}
		// Node has finished draining successfully if replicas=0
		// otherwise we will signal an error, so the backoff logic retry until replicas=0 or timeout
		if replicas != 0 {
			return errors.Wrap(err, fmt.Sprintf("node %d has not completed draining yet", id))
		}
	}

	return nil
}

// RequireDatabaseToFunctionInsecure tests that the database is functioning correctly on an
// db that is insecure.
func RequireDatabaseToFunctionInsecure(t *testing.T, sb testenv.DiffingSandbox, b ClusterBuilder) {
	requireDatabaseToFunction(t, sb, b, false)
}

// RequireDatabaseToFunction tests that the database is functioning correctly
// for a db cluster that is using an SSL certificate.
func RequireDatabaseToFunction(t *testing.T, sb testenv.DiffingSandbox, b ClusterBuilder) {
	requireDatabaseToFunction(t, sb, b, true)
}

func requireDatabaseToFunction(t *testing.T, sb testenv.DiffingSandbox, b ClusterBuilder, useSSL bool) {
	t.Log("Testing database function")
	sb.Mgr.GetConfig()
	podName := fmt.Sprintf("%s-0.%s", b.Cluster().Name(), b.Cluster().Name())

	conn := &database.DBConnection{
		Ctx:    context.TODO(),
		Client: sb.Mgr.GetClient(),
		Port:   b.Cluster().Spec().SQLPort,
		UseSSL: useSSL,

		RestConfig:   sb.Mgr.GetConfig(),
		ServiceName:  podName,
		Namespace:    sb.Namespace,
		DatabaseName: "system",

		RunningInsideK8s: false,
	}

	// set the client certs since we are using SSL
	if useSSL {
		conn.ClientCertificateSecretName = b.Cluster().ClientTLSSecretName("root")
		conn.RootCertificateSecretName = b.Cluster().NodeTLSSecretName()
	}

	// Create a new database connection for the update.
	db, err := database.NewDbConnection(conn)
	require.NoError(t, err)
	defer db.Close()

	t.Log("DB connection initialized; running commands")

	if _, err := db.Exec("CREATE DATABASE test_db"); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec("USE test_db"); err != nil {
		t.Fatal(err)
	}

	// Create the "accounts" table.
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS accounts (id INT PRIMARY KEY, balance INT)"); err != nil {
		t.Fatal(err)
	}

	// Insert two rows into the "accounts" table.
	if _, err := db.Exec(
		"INSERT INTO accounts (id, balance) VALUES (1, 1000), (2, 250)"); err != nil {
		t.Fatal(err)
	}

	// Print out the balances.
	rows, err := db.Query("SELECT id, balance FROM accounts")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	t.Log("Initial balances:")
	for rows.Next() {
		var id, balance int
		if err := rows.Scan(&id, &balance); err != nil {
			t.Fatal(err)
		}
		t.Log("balances", id, balance)
	}

	countRows, err := db.Query("SELECT COUNT(*) as count FROM accounts")
	if err != nil {
		t.Fatal(err)
	}
	defer countRows.Close()
	count := getCount(t, countRows)
	if count != 2 {
		t.Fatal(fmt.Errorf("found incorrect number of rows.  Expected 2 got %v", count))
	}

	t.Log("finished testing database")
}

func getCount(t *testing.T, rows *sql.Rows) (count int) {
	for rows.Next() {
		err := rows.Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
	}
	return count
}

// RequirePVCToResize checks that the PVCs are resized correctly
func RequirePVCToResize(t *testing.T, ctx context.Context, sb testenv.DiffingSandbox, b ClusterBuilder, quantity apiresource.Quantity) {
	pvcsToKeep, err := fetchPVCsToKeep(ctx, sb, b)
	require.Nil(t, err)

	pvcList, err := fetchPVCs(ctx, sb, b)
	require.Nil(t, err)

	for _, pvc := range pvcList.Items {
		t.Logf("checking pvc %s", pvc.Name)
		// Resize PVCs that are still in use
		if pvcsToKeep[pvc.Name] {
			require.True(t, pvc.Spec.Resources.Requests.Storage().Equal(quantity))
		}
	}
}

func fetchPVCsToKeep(ctx context.Context, sb testenv.DiffingSandbox, b ClusterBuilder) (map[string]bool, error) {
	cluster := b.Cluster()
	var prefixes []string
	var pvcsToKeep map[string]bool

	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, 500*time.Second, true, func(ctx context.Context) (bool, error) {
		ss, err := fetchStatefulSet(sb, cluster.StatefulSetName())
		if err != nil {
			return false, err
		}
		if !statefulSetIsReady(ss) {
			return false, nil
		}

		prefixes = make([]string, len(ss.Spec.VolumeClaimTemplates))
		pvcsToKeep = make(map[string]bool, int(*ss.Spec.Replicas)*len(ss.Spec.VolumeClaimTemplates))
		for i, pvct := range ss.Spec.VolumeClaimTemplates {
			prefixes[i] = fmt.Sprintf("%s-%s-", pvct.Name, ss.Name)
			for j := int32(0); j < *ss.Spec.Replicas; j++ {
				name := fmt.Sprintf("%s-%s-%d", pvct.Name, ss.Name, j)
				pvcsToKeep[name] = true
			}
		}

		return true, nil
	})

	if err != nil {
		return nil, err
	}
	return pvcsToKeep, nil
}

func logPods(ctx context.Context, sts *appsv1.StatefulSet, cluster *resource.Cluster,
	sb testenv.DiffingSandbox, t *testing.T) error {
	// create a new clientset to talk to k8s
	clientset, err := kubernetes.NewForConfig(sb.Mgr.GetConfig())
	if err != nil {
		return err
	}

	// the LabelSelector I thought worked did not
	// so I just get all of the Pods in a NS
	options := metav1.ListOptions{
		//LabelSelector: "app=" + cluster.StatefulSetName(),
	}

	// Get all pods
	podList, err := clientset.CoreV1().Pods(sts.Namespace).List(ctx, options)
	if err != nil {
		return err
	}

	if len(podList.Items) == 0 {
		t.Log("no pods found")
	}

	// Print out pretty into on the Pods
	for _, podInfo := range (*podList).Items {
		t.Logf("pods-name=%v\n", podInfo.Name)
		t.Logf("pods-status=%v\n", podInfo.Status.Phase)
		t.Logf("pods-condition=%v\n", podInfo.Status.Conditions)
		/*
			// TODO if pod is running but not ready for some period get pod logs
			if kube.IsPodReady(&podInfo) {
				t.Logf("pods-condition=%v\n", podInfo.Status.Conditions)
			}
		*/
	}

	return nil
}

// RequireNumberOfPVCs checks that the correct number of PVCs are claimed
func RequireNumberOfPVCs(t *testing.T, ctx context.Context, sb testenv.DiffingSandbox, b ClusterBuilder, quantity int) {
	pvcList, err := fetchPVCs(ctx, sb, b)
	require.Nil(t, err)

	boundPVCCount := 0
	for _, pvc := range pvcList.Items {
		if pvc.Status.Phase == corev1.ClaimBound {
			boundPVCCount = boundPVCCount + 1
		}
	}
	require.Equal(t, quantity, boundPVCCount)
}

// HasNumPVCs returns true when the number of bound PVCs is equal to the supplied
// quantity. If an error occurs, it returns false.
func HasNumPVCs(ctx context.Context, sb testenv.DiffingSandbox, b ClusterBuilder, quantity int) bool {
	pvcList, err := fetchPVCs(ctx, sb, b)
	if err != nil {
		return false
	}

	boundPVCCount := 0
	for _, pvc := range pvcList.Items {
		if pvc.Status.Phase == corev1.ClaimBound {
			boundPVCCount = boundPVCCount + 1
		}
	}

	return boundPVCCount == quantity
}

func fetchPVCs(ctx context.Context, sb testenv.DiffingSandbox, b ClusterBuilder) (*corev1.PersistentVolumeClaimList, error) {
	cluster := b.Cluster()
	var pvcList *corev1.PersistentVolumeClaimList

	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, 500*time.Second, true, func(ctx context.Context) (bool, error) {
		clientset, err := kubernetes.NewForConfig(sb.Mgr.GetConfig())
		if err != nil {
			return false, err
		}

		sts, err := fetchStatefulSet(sb, cluster.StatefulSetName())
		if err != nil {
			return false, err
		}

		pvcList, err = clientset.CoreV1().PersistentVolumeClaims(cluster.Namespace()).List(ctx, metav1.ListOptions{
			LabelSelector: metav1.FormatLabelSelector(sts.Spec.Selector),
		})
		if err != nil {
			return false, err
		}
		return true, nil
	})

	if err != nil {
		return nil, err
	}
	return pvcList, nil
}

// RequireClusterInImagePullBackoff checks that the CRDB cluster should be either in ImagePullBackOff/ErrImagePull state
func RequireClusterInImagePullBackoff(t *testing.T, sb testenv.DiffingSandbox, b ClusterBuilder) {
	clusterName := b.Cluster().Name()
	var jobList = &batchv1.JobList{}
	var podList = &corev1.PodList{}
	var re = regexp.MustCompile(`ErrImagePull|ImagePullBackOff`)
	jobLabel := map[string]string{
		"app.kubernetes.io/instance": clusterName,
	}

	// Timeout must be greater than 2 minutes, the max backoff time for the
	// version checker job.
	ctx := context.Background()
	wErr := wait.PollUntilContextTimeout(ctx, 10*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := sb.List(jobList, jobLabel); err != nil {
			return false, err
		}

		if len(jobList.Items) == 0 {
			t.Logf("No validation job found for the CRDB cluster")
			return false, nil
		}

		return true, nil
	})

	require.NoError(t, wErr)

	podLabel := map[string]string{
		"job-name": jobList.Items[0].Name,
	}

	if err := sb.List(podList, podLabel); err != nil {
		require.NoError(t, err)
	}

	if podList.Items[0].Status.Phase == corev1.PodPending && len(podList.Items[0].Status.ContainerStatuses) != 0 {
		require.True(t, re.MatchString(podList.Items[0].Status.ContainerStatuses[0].State.Waiting.Reason))
	}
}

// RequireClusterInFailedState check that the crdbclusters CR is marked as Failed state.
func RequireClusterInFailedState(t *testing.T, sb testenv.DiffingSandbox, b ClusterBuilder) {
	var crdbCluster = api.CrdbCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.Cluster().Name(),
			Namespace: b.Cluster().Namespace(),
		},
	}

	ctx := context.Background()
	wErr := wait.PollUntilContextTimeout(ctx, 10*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := sb.Get(&crdbCluster); err != nil {
			return false, err
		}

		if crdbCluster.Status.ClusterStatus == "Failed" {
			return true, nil
		}

		return false, nil
	})

	t.Logf("CRDB Cluster status: %s", crdbCluster.Status.ClusterStatus)
	require.NoError(t, wErr)
}

func RequireLoggingConfigMap(t *testing.T, sb testenv.DiffingSandbox, name string, logConfig string) {
	var loggingConfigMap = corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: sb.Namespace,
		},
		Data: map[string]string{
			"logging.yaml": logConfig,
		},
	}

	err := sb.Create(&loggingConfigMap)
	require.NoError(t, err)
}
