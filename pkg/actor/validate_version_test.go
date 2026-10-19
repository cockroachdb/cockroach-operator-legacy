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
	"testing"

	api "github.com/cockroachdb/cockroach-operator/apis/v1alpha1"
	"github.com/cockroachdb/cockroach-operator/pkg/actor"
	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/cockroachdb/cockroach-operator/pkg/testutil"
	"github.com/cockroachdb/errors"
	"github.com/go-logr/zapr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// Versions below v21.1 keep IsLoggingAPIEnabled false, so Act does not shell
// out to `cockroach debug check-log-config`.
const (
	testOldVersion = "v20.2.8"
	testNewVersion = "v20.2.9"
	testOldImage   = "cockroachdb/cockroach:v20.2.8"
	testNewImage   = "cockroachdb/cockroach:v20.2.9"
)

func setSupportedTestVersions(t *testing.T) {
	t.Helper()
	t.Setenv("RELATED_IMAGE_COCKROACH_v20_2_8", testOldImage)
	t.Setenv("RELATED_IMAGE_COCKROACH_v20_2_9", testNewImage)
}

// fetchCluster re-reads the CrdbCluster from the fake API server so assertions
// see what was persisted, not what the in-memory wrapper holds.
func fetchCluster(t *testing.T, cl *testutil.FakeClient, namespace, name string) *resource.Cluster {
	t.Helper()
	got := &api.CrdbCluster{}
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, got))
	c := resource.NewCluster(got)
	return &c
}

// TestVersionCheckerCockroachDBVersionWritesAnnotations covers the
// cockroachDBVersion (RELATED_IMAGE) path. The image.name path writes the
// crdb.io/version and crdb.io/containerimage annotations; this path must too,
// because director.needsPartitionedUpdate reads them.
func TestVersionCheckerCockroachDBVersionWritesAnnotations(t *testing.T) {
	setSupportedTestVersions(t)
	ctx := context.Background()
	scheme := testutil.InitScheme(t)

	cluster := testutil.NewBuilder("cockroachdb").
		Namespaced("default").
		WithUID("cockroachdb-uid").
		WithNodeCount(1).
		WithCockroachDBVersion(testNewVersion).
		Cluster()
	cluster.SetFalse(api.CrdbVersionChecked)

	cl := testutil.NewFakeClient(scheme, cluster.Unwrap())
	checker := actor.NewVersionChecker(scheme, cl, fake.NewSimpleClientset())

	require.NoError(t, checker.Act(ctx, cluster, zapr.NewLogger(zaptest.NewLogger(t))))

	got := fetchCluster(t, cl, "default", "cockroachdb")
	require.Equal(t, testNewVersion, got.GetVersionAnnotation(), "crdb.io/version annotation")
	require.Equal(t, testNewImage, got.GetAnnotationContainerImage(), "crdb.io/containerimage annotation")
	require.Equal(t, testNewVersion, got.Status().Version)
	require.Equal(t, testNewImage, got.Status().CrdbContainerImage)
	require.True(t, got.True(api.CrdbVersionChecked))
}

// TestCompleteVersionCheckerKeepsAnnotationsAndStatusInSync pins the shared
// tail of the version checker: whatever it records in status it records in
// the annotations too. If the annotation write ever moves back out of this
// shared function into one branch of Act, this test fails;
// TestNeedsPartitionedUpdateAfterCockroachDBVersionBump covers the
// cockroachDBVersion branch actually reaching it.
func TestCompleteVersionCheckerKeepsAnnotationsAndStatusInSync(t *testing.T) {
	ctx := context.Background()
	scheme := testutil.InitScheme(t)

	// completeVersionChecker reads only the cluster's name and namespace.
	cluster := testutil.NewBuilder("cockroachdb").
		Namespaced("default").
		WithUID("cockroachdb-uid").
		WithNodeCount(1).
		Cluster()
	cluster.SetFalse(api.CrdbVersionChecked)

	cl := testutil.NewFakeClient(scheme, cluster.Unwrap())
	checker := actor.NewVersionChecker(scheme, cl, fake.NewSimpleClientset())

	require.NoError(t, actor.CompleteVersionChecker(ctx, checker, cluster, testOldVersion, testOldImage,
		zapr.NewLogger(zaptest.NewLogger(t))))

	got := fetchCluster(t, cl, "default", "cockroachdb")
	require.Equal(t, got.Status().Version, got.GetVersionAnnotation(),
		"crdb.io/version must match status.version")
	require.Equal(t, got.Status().CrdbContainerImage, got.GetAnnotationContainerImage(),
		"crdb.io/containerimage must match status.crdbcontainerimage")
	require.Equal(t, testOldVersion, got.Status().Version)
	require.Equal(t, testOldImage, got.Status().CrdbContainerImage)
	require.True(t, got.True(api.CrdbVersionChecked))
}

// TestVersionCheckerAnnotationWriteFailureLeavesVersionUnchecked covers the
// write ordering: when the annotation write fails, the status write must not
// run, so CrdbVersionChecked stays false and the controller re-runs the
// checker instead of believing the check completed.
func TestVersionCheckerAnnotationWriteFailureLeavesVersionUnchecked(t *testing.T) {
	setSupportedTestVersions(t)
	ctx := context.Background()
	scheme := testutil.InitScheme(t)

	cluster := testutil.NewBuilder("cockroachdb").
		Namespaced("default").
		WithUID("cockroachdb-uid").
		WithNodeCount(1).
		WithCockroachDBVersion(testNewVersion).
		Cluster()
	cluster.SetFalse(api.CrdbVersionChecked)

	cl := testutil.NewFakeClient(scheme, cluster.Unwrap())
	injected := errors.New("injected annotation write failure")
	cl.AddReactor("update", "crdbclusters", func(testutil.Action) (bool, error) { return true, injected })
	checker := actor.NewVersionChecker(scheme, cl, fake.NewSimpleClientset())

	err := checker.Act(ctx, cluster, zapr.NewLogger(zaptest.NewLogger(t)))
	require.ErrorIs(t, err, injected)

	got := fetchCluster(t, cl, "default", "cockroachdb")
	require.False(t, got.True(api.CrdbVersionChecked), "status must not say checked when the annotation write failed")
	require.Empty(t, got.GetVersionAnnotation())
	require.Empty(t, got.Status().Version)
}

// newVersionBumpTestEnv builds a steady-state cluster (see newTestDirector)
// that is running at the old version and has just been asked for the new one
// via cockroachDBVersion. The CrdbCluster itself is stored in the fake API
// server because the version checker fetches and updates it.
func newVersionBumpTestEnv(t *testing.T) (*resource.Cluster, actor.Director, *testutil.FakeClient) {
	t.Helper()
	cluster, director, cl, _ := newTestDirector(t, testDirectorOptions{
		builder: func(b testutil.ClusterBuilder) testutil.ClusterBuilder {
			// Start without a crdb.io/version annotation: the checker not
			// writing it is the bug under test.
			return b.
				WithClusterAnnotations(map[string]string{}).
				WithCockroachDBVersion(testNewVersion)
		},
		mutateCluster: func(c *resource.Cluster) {
			// The controller has re-armed the version check for the new spec.
			c.SetFalse(api.CrdbVersionChecked)
		},
		statefulSetAnnotations: map[string]string{
			resource.CrdbVersionAnnotation:        testOldVersion,
			resource.CrdbContainerImageAnnotation: testOldImage,
		},
		seedClusterInClient: true,
	})
	return cluster, director, cl
}

// TestNeedsPartitionedUpdateAfterCockroachDBVersionBump walks the director
// through a version bump made with spec.cockroachDBVersion: first it must run
// the version checker, and once the checker has recorded the new version the
// director must choose the partitioned update, not a plain Deploy.
func TestNeedsPartitionedUpdateAfterCockroachDBVersionBump(t *testing.T) {
	setSupportedTestVersions(t)
	ctx := context.Background()
	log := zapr.NewLogger(zaptest.NewLogger(t))
	cluster, director, cl := newVersionBumpTestEnv(t)

	chosen, err := director.GetActorToExecute(ctx, cluster, log)
	require.NoError(t, err)
	require.NotNil(t, chosen)
	require.Equal(t, api.VersionCheckerAction, chosen.GetActionType())

	require.NoError(t, chosen.Act(ctx, cluster, log))

	refreshed := fetchCluster(t, cl, "default", "cockroachdb")
	require.True(t, refreshed.True(api.CrdbVersionChecked))
	require.Equal(t, testNewVersion, refreshed.GetVersionAnnotation())
	chosen, err = director.GetActorToExecute(ctx, refreshed, log)
	require.NoError(t, err)
	require.NotNil(t, chosen)
	require.Equal(t, api.PartitionedUpdateAction, chosen.GetActionType(),
		"a version bump via cockroachDBVersion must take the partitioned update path")
}
