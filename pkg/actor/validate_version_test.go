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
	"github.com/go-logr/zapr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"k8s.io/apimachinery/pkg/types"
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
// the version checker, then it picks the actor for the actual version change.
//
// This test currently pins the bug. A version bump made with
// spec.cockroachDBVersion runs the version checker, but the checker never
// writes the crdb.io/version annotation on that path, so the director falls
// through to a plain Deploy instead of the partitioned update. The next
// commit fixes the checker and flips these two assertions.
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
	require.Empty(t, refreshed.GetVersionAnnotation())
	chosen, err = director.GetActorToExecute(ctx, refreshed, log)
	require.NoError(t, err)
	require.NotNil(t, chosen)
	require.Equal(t, api.DeployAction, chosen.GetActionType())
}
