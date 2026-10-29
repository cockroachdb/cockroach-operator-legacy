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
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	api "github.com/cockroachdb/cockroach-operator/apis/v1alpha1"
	"github.com/cockroachdb/cockroach-operator/pkg/kube"
	"github.com/cockroachdb/cockroach-operator/pkg/labels"
	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/cockroachdb/cockroach-operator/pkg/testutil"
	"github.com/cockroachdb/cockroach-operator/pkg/utilfeature"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var update = flag.Bool("update", false, "update the golden files of this test")

func TestStatefulSetBuilder(t *testing.T) {
	// turn on featuregate to test rules
	require.NoError(t, utilfeature.DefaultMutableFeatureGate.Set("AffinityRules=true,TolerationRules=true"))
	sc := testutil.InitScheme(t)

	decoder, encoder := testutil.Yamlizers(t, sc)

	inputSuffix := "_in.yaml"
	folder := filepath.Join("testdata", filepath.FromSlash(t.Name()))
	testInputs := filepath.Join(folder, "/*"+inputSuffix)
	clusterFiles, err := filepath.Glob(testInputs)
	if err != nil || len(clusterFiles) == 0 {
		t.Fatalf("failed to find cluster specs %s: %v", testInputs, err)
	}

	for _, inFile := range clusterFiles {
		testName := inFile[len(folder)+1 : len(inFile)-len(inputSuffix)]
		clusterObj := decoder(load(t, inFile))
		cr, ok := clusterObj.(*api.CrdbCluster)
		if !ok {
			t.Fatal("failed to deserialize CrdbCluster")
		}
		commonLabels := labels.Common(cr)
		cluster := resource.NewCluster(cr)

		t.Run(testName, func(t *testing.T) {
			actual := &appsv1.StatefulSet{}

			err := resource.StatefulSetBuilder{
				Cluster:   &cluster,
				Selector:  commonLabels.Selector(cluster.Spec().AdditionalLabels),
				Telemetry: "kubernetes-operator-gke",
			}.Build(actual)
			require.NoError(t, err)

			var buf bytes.Buffer
			err = encoder(actual, &buf)
			require.NoError(t, err)

			expectedStr := testutil.ReadOrUpdateGoldenFile(t, buf.String(), *update)
			expected := decoder([]byte(expectedStr))

			diff := cmp.Diff(expected, actual, testutil.RuntimeObjCmpOpts...)
			if diff != "" {
				assert.Fail(t, fmt.Sprintf("unexpected result (-want +got):\n%v", diff))
			}
		})
	}
}

func TestRHImage(t *testing.T) {
	rhImage := "redhat-coachroach-test:v22"
	// os.Setenv(resource.RhEnvVar, rhImage)

	cluster := resource.NewCluster(&api.CrdbCluster{
		Spec: api.CrdbClusterSpec{
			Image: &api.PodImage{},
		},
	})

	b := resource.StatefulSetBuilder{
		Cluster: &cluster,
	}

	container := b.MakeContainers()
	//we should run the version checker to set this field so it is empty
	if container[0].Image != "" {
		assert.Fail(t, fmt.Sprintf("unexpected result expected image to equal: %s, got: %s", rhImage, container[0].Image))
	}

}

func load(t *testing.T, file string) []byte {
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("failed to load yaml file %s: %v", file, err)
	}

	return content
}

// An unrelated change writes the StatefulSet. That write must keep the restart
// marker, or every pod rolls a second time.
func TestStatefulSetKeepsRestartMarkerOnRedeploy(t *testing.T) {
	ctx := context.Background()
	scheme := testutil.InitScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	deploy := func(nodes int32) (bool, error) {
		cluster := testutil.NewBuilder("test-cluster").Namespaced("default").WithUID("uid").
			WithNodeCount(nodes).WithPVDataStore("1Gi").Cluster()
		return resource.Reconciler{
			ManagedResource: resource.NewManagedKubeResource(ctx, cl, cluster, kube.AnnotatingPersister),
			Builder: resource.StatefulSetBuilder{
				Cluster:  cluster,
				Selector: labels.Common(cluster.Unwrap()).Selector(cluster.Spec().AdditionalLabels),
			},
			Owner:  cluster.Unwrap(),
			Scheme: scheme,
		}.Reconcile()
	}

	_, err := deploy(1)
	require.NoError(t, err)

	// The marker the restart actor stamps onto the live pod template.
	key := client.ObjectKey{Namespace: "default", Name: "test-cluster"}
	deployed := &appsv1.StatefulSet{}
	require.NoError(t, cl.Get(ctx, key, deployed))
	deployed.Spec.Template.Annotations = map[string]string{
		resource.CrdbRestartAnnotation: "2026-01-01T00:00:00Z",
	}
	require.NoError(t, cl.Update(ctx, deployed))

	// A node count change writes the StatefulSet but must not touch the pods.
	changed, err := deploy(3)
	require.NoError(t, err)
	require.True(t, changed)

	require.NoError(t, cl.Get(ctx, key, deployed))
	require.Equal(t, "2026-01-01T00:00:00Z", deployed.Spec.Template.Annotations[resource.CrdbRestartAnnotation])
}
