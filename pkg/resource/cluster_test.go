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

	"fmt"

	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/cockroachdb/cockroach-operator/pkg/testutil"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
)

func TestClusterTLSSecrets(t *testing.T) {
	var (
		testCluster = "test-cluster"
		testNS      = "test-ns"

		customNodeTLS   = "custom-node-tls"
		customClientTLS = "custom-client-tls"
	)

	clusterBuilder := testutil.NewBuilder(testCluster).Namespaced(testNS)

	for _, tt := range []struct {
		name                string
		cluster             *resource.Cluster
		nodeTLSSecretName   string
		clientTLSSecretName string
	}{
		{
			name:              "verify default node tls cert",
			cluster:           clusterBuilder.Cluster(),
			nodeTLSSecretName: "test-cluster-node",
		},
		{
			name:                "verify default client tls cert",
			cluster:             clusterBuilder.Cluster(),
			clientTLSSecretName: "test-cluster-root",
		},
		{
			name:              "verify custom node tls cert",
			cluster:           clusterBuilder.WithNodeTLS(customNodeTLS).Cluster(),
			nodeTLSSecretName: customNodeTLS,
		},
		{
			name:                "verify custom client tls cert",
			cluster:             clusterBuilder.WithClientTLS(customClientTLS).Cluster(),
			clientTLSSecretName: customClientTLS,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var expected, actual string

			if tt.nodeTLSSecretName != "" {
				expected = tt.nodeTLSSecretName
				actual = tt.cluster.NodeTLSSecretName()

			}

			if tt.clientTLSSecretName != "" {
				expected = tt.clientTLSSecretName
				actual = tt.cluster.ClientTLSSecretName("root")
			}

			diff := cmp.Diff(expected, actual, testutil.RuntimeObjCmpOpts...)
			if diff != "" {
				assert.Fail(t, fmt.Sprintf("unexpected result (-want +got):\n%v", diff))
			}
		})
	}
}
