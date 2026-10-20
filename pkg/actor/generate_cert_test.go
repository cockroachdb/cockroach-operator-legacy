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
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"

	api "github.com/cockroachdb/cockroach-operator/apis/v1alpha1"
	"github.com/cockroachdb/cockroach-operator/pkg/actor"
	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/cockroachdb/cockroach-operator/pkg/testutil"
	"github.com/cockroachdb/cockroach-operator/pkg/testutil/env"
	"github.com/go-logr/zapr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestMain puts the bazel-provided cockroach binary on PATH; certificate
// generation shells out to it (see //hack/bin:cockroach in BUILD.bazel).
// Outside bazel, env.ExpandPath shells out to `bazel info workspace` and
// panics without bazel, the same as pkg/security.
func TestMain(m *testing.M) {
	env.PrependToPath(env.ExpandPath("hack/bin"))
	os.Exit(m.Run())
}

// newCertTestEnv returns a secure one-node cluster stored in a fake API
// server, and the certificate actor bound to that server. The
// controller-runtime fake is used directly, with the status subresource
// registered, because Act writes annotations with Update and conditions with
// Status().Update and the test must observe both.
func newCertTestEnv(t *testing.T) (*resource.Cluster, client.Client, actor.Actor) {
	t.Helper()
	scheme := testutil.InitScheme(t)
	cluster := testutil.NewBuilder("cockroachdb").
		Namespaced("default").
		WithUID("cockroachdb-uid").
		WithNodeCount(1).
		WithTLS().
		Cluster()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&api.CrdbCluster{}).
		WithObjects(cluster.Unwrap()).
		Build()
	return cluster, cl, actor.NewGenerateCert(cl)
}

// refetchCluster re-reads the CrdbCluster so assertions and the next Act see
// what was persisted, not what the previous wrapper held.
func refetchCluster(t *testing.T, cl client.Client, cluster *resource.Cluster) *resource.Cluster {
	t.Helper()
	cr := &api.CrdbCluster{}
	key := types.NamespacedName{Namespace: cluster.Namespace(), Name: cluster.Name()}
	require.NoError(t, cl.Get(context.Background(), key, cr))
	c := resource.NewCluster(cr)
	return &c
}

// nodeCertPEM returns the PEM-encoded node certificate stored in the node TLS
// secret; comparing PEM bytes across Act calls detects regeneration.
func nodeCertPEM(t *testing.T, cl client.Client, cluster *resource.Cluster) []byte {
	t.Helper()
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: cluster.Namespace(), Name: cluster.NodeTLSSecretName()}
	require.NoError(t, cl.Get(context.Background(), key, secret))
	return secret.Data[corev1.TLSCertKey]
}

// dnsNames parses a PEM certificate and returns its SAN DNS names.
func dnsNames(t *testing.T, pemCert []byte) []string {
	t.Helper()
	block, _ := pem.Decode(pemCert)
	require.NotNil(t, block, "node secret must hold a PEM certificate")
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert.DNSNames
}

// TestGenerateCertRequestsRestartWhenLiveCertChanges walks the certificate
// actor through a cluster's life: first generation, then a change that
// regenerates a certificate the pods already hold.
//
// It documents the current behavior: adding the first SQL ingress host
// regenerates the node certificate (status.sqlHost differs from the spec)
// but requests no restart, because the restart decision keys on the SQL
// ingress condition, which the ingress actor only sets later. The next
// commit fixes that and flips the final assertion.
func TestGenerateCertRequestsRestartWhenLiveCertChanges(t *testing.T) {
	ctx := context.Background()
	log := zapr.NewLogger(zaptest.NewLogger(t))
	cluster, cl, act := newCertTestEnv(t)

	// First generation: no pods exist yet, so no restart is requested.
	require.NoError(t, act.Act(ctx, cluster, log))
	cluster = refetchCluster(t, cl, cluster)
	require.True(t, cluster.True(api.CertificateGenerated))
	require.Empty(t, cluster.GetAnnotationRestartType(), "first generation must not request a restart")
	firstCert := nodeCertPEM(t, cl, cluster)

	// Expose SQL ingress for the first time. The ingress actor has not run
	// yet, so the SQL-ingress condition is still false.
	cr := cluster.Unwrap()
	cr.Spec.Ingress = &api.IngressConfig{SQL: &api.Ingress{Host: "sql.example.com"}}
	require.NoError(t, cl.Update(ctx, cr))
	cluster = refetchCluster(t, cl, cluster)

	require.NoError(t, act.Act(ctx, cluster, log))
	cluster = refetchCluster(t, cl, cluster)
	secondCert := nodeCertPEM(t, cl, cluster)
	require.Contains(t, dnsNames(t, secondCert), "sql.example.com", "node cert must be regenerated with the new host")
	require.NotEqual(t, firstCert, secondCert)
	require.Equal(t, "sql.example.com", cluster.Status().SQLHost)

	// Current behavior: the live certificate changed but no restart was
	// requested, so pods keep serving the old certificate.
	require.Empty(t, cluster.GetAnnotationRestartType())
}
