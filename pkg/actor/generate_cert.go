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
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	api "github.com/cockroachdb/cockroach-operator/apis/v1alpha1"
	"github.com/cockroachdb/cockroach-operator/pkg/kube"
	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/cockroachdb/cockroach-operator/pkg/security"
	"github.com/cockroachdb/cockroach-operator/pkg/util"
	"github.com/cockroachdb/errors"
	"github.com/go-logr/logr"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Options settable via command-line flags. See below for defaults.
var caCertificateLifetime time.Duration
var certificateLifetime time.Duration
var allowCAKeyReuse bool
var overwriteFiles bool
var generatePKCS8Key bool

func newGenerateCert(cl client.Client) Actor {
	return &generateCert{
		action: newAction(nil, cl, nil, nil),
	}
}

// generateCert issues node and root client certificates via Kubernetes cluster CA
type generateCert struct {
	action

	CertsDir string
	CAKey    string
}

// GetActionType returns api.RequestCertAction action used to set the cluster status errors
func (rc *generateCert) GetActionType() api.ActionType {
	return api.GenerateCertAction
}

// Act func generates the various certificates required and then stores
// the certificates in secrets.
func (rc *generateCert) Act(ctx context.Context, cluster *resource.Cluster, log logr.Logger) error {
	if !cluster.Spec().TLSEnabled || cluster.Spec().NodeTLSSecret != "" {
		log.V(DEBUGLEVEL).Info("Skipping TLS cert generation", "enabled", cluster.Spec().TLSEnabled, "secret", cluster.Spec().NodeTLSSecret)
		return nil
	}

	// create the various temporary directories to store the certificates in
	// the directors will delete when the code is completed.
	certsDir, cleanup := util.CreateTempDir("certsDir")
	defer cleanup()
	rc.CertsDir = certsDir

	caDir, cleanupCADir := util.CreateTempDir("caDir")
	defer cleanupCADir()
	rc.CAKey = filepath.Join(caDir, "ca.key")

	// generate the base CA cert and key
	if err := rc.generateCA(ctx, log, cluster); err != nil {
		msg := "error generating CA"
		log.Error(err, msg)
		return errors.Wrap(err, msg)
	}
	// generate the node certificate for the database to use
	expirationDate, replacedLiveCert, err := rc.generateNodeCert(ctx, log, cluster)
	if err != nil {
		msg := "error generating Node Certificate"
		log.Error(err, msg)
		return errors.Wrap(err, msg)
	}

	// TODO if we save the node certificate but error on saving the client
	// certificate should we delete the node secret?

	// generate the client certificates for the database to use
	if err := rc.generateClientCert(ctx, log, cluster); err != nil {
		msg := "error generating Client Certificate"
		log.Error(err, msg)
		return errors.Wrap(err, msg)
	}

	// A regenerated live certificate reaches the pods only through a restart;
	// see generateNodeCert for why. Keying the restart on the regeneration
	// itself, not on the SQL-ingress condition the ingress actor sets only
	// after this actor runs, means every reason to regenerate gets one.
	//
	// Write the cert expiration annotation to the object. This is an annotation, which is NOT on the CrdbClusterStatus
	// object, so we need to call rc.client.Update(ctx, crdbobj).
	fetcher := resource.NewKubeFetcher(ctx, cluster.Namespace(), rc.client)
	// The restart annotation is persisted before status.sqlHost on purpose: if
	// the status write fails, the next run regenerates and requests one more
	// restart, rather than recording the host as done with no restart
	// requested.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		newcr := resource.ClusterPlaceholder(cluster.Name())
		if err := fetcher.Fetch(newcr); err != nil {
			msg := "failed to retrieve CrdbCluster resource"
			log.Error(err, msg)
			return errors.Wrap(err, msg)
		}
		refreshedCluster := resource.NewCluster(newcr)
		refreshedCluster.Fetcher = fetcher
		refreshedCluster.SetAnnotationCertExpiration(expirationDate)
		if replacedLiveCert {
			refreshedCluster.SetRestartTypeAnnotation(api.ClusterRestartType(api.RollingRestart).String())
		}
		crdbobj := refreshedCluster.Unwrap()

		err := rc.client.Update(ctx, crdbobj)
		if err != nil {
			msg := "failed updating the annotations on request certificate will try again"
			log.Error(err, msg)
			return errors.Wrap(err, msg)
		}
		return err
	})
	if err != nil {
		msg := "failed saving the annotations on request certificate"
		log.Error(err, msg)
		return errors.Wrap(err, msg)
	}

	// Write the certificate generated condition to the object. This condition IS on the CrdbClusterStatus object,
	// so we need to call rc.client.Status().Update(ctx, crdbobj).
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		newcr := resource.ClusterPlaceholder(cluster.Name())
		if err := fetcher.Fetch(newcr); err != nil {
			msg := "failed to retrieve CrdbCluster resource"
			log.Error(err, msg)
			return errors.Wrap(err, msg)
		}
		refreshedCluster := resource.NewCluster(newcr)
		refreshedCluster.Fetcher = fetcher
		refreshedCluster.SetTrue(api.CertificateGenerated)
		if cluster.IsSQLIngressEnabled() {
			refreshedCluster.SetSQLHost(cluster.Spec().Ingress.SQL.Host)
		}

		crdbobj := refreshedCluster.Unwrap()
		err = rc.client.Status().Update(ctx, crdbobj)
		if err != nil {
			msg := "failed saving the status on generate cert"
			log.Error(err, msg)
			return errors.Wrap(err, msg)
		}
		return err
	})
	if err != nil {
		msg := "failed saving the status on generate cert"
		log.Error(err, msg)
		return errors.Wrap(err, msg)
	}

	return nil
}

func (rc *generateCert) generateCA(ctx context.Context, log logr.Logger, cluster *resource.Cluster) error {
	log.V(DEBUGLEVEL).Info("generating CA")
	// load the secret.  If it exists don't update the cert
	secret, err := resource.LoadTLSSecret(cluster.CASecretName(),
		resource.NewKubeResource(ctx, rc.client, cluster.Namespace(), kube.DefaultPersister))

	if kube.IgnoreNotFound(err) != nil {
		return errors.Wrap(err, "failed to get ca key secret")
	}
	// if the secret is ready then don't update the secret
	// the Actor should have already generated the secret
	if secret.ReadyCA() {
		if err := os.WriteFile(rc.CAKey, secret.CAKey(), 0600); err != nil {
			return errors.Wrap(err, "failed to write CA key")
		}
		log.V(DEBUGLEVEL).Info("not updating ca key as it exists")
		return nil
	}

	err = errors.Wrap(
		security.CreateCAPair(
			rc.CertsDir,
			rc.CAKey,
			caCertificateLifetime,
			allowCAKeyReuse,
			overwriteFiles),
		"failed to generate CA cert and key")
	if err != nil {
		return err
	}
	// Read the ca key into memory
	cakey, err := os.ReadFile(rc.CAKey)
	if err != nil {
		return errors.Wrap(err, "unable to read ca.key")
	}

	// create and save the TLS certificates into a secret
	secret = resource.CreateTLSSecret(cluster.CASecretName(),
		resource.NewKubeResource(ctx, rc.client, cluster.Namespace(), kube.DefaultPersister))

	if err = secret.UpdateCAKey(cakey, log); err != nil {
		return errors.Wrap(err, "failed to update ca key secret ")
	}

	log.V(DEBUGLEVEL).Info("generated and saved ca key")
	return nil
}

// TODO we have an edge case that exists that the actor is not handling properly
// If any errors occurs and we have save secrets we may need to delete the secrets
// We can get into a race condition where the Node certifcate was created, but the Client certificate was not.
// Errors are thrown, and then the actor runs again.
// This time a new CA is generated, the Node secret is not updated, but the client certicate is generated
// using a new CA.

// generateNodeCert generates the node key and certificate and stores them in
// the node TLS secret. It returns the certificate's expiration date and
// whether it replaced a certificate that a ready secret already held. Pods
// copy certificates into an emptyDir at init and never re-read them, so any
// regeneration of a live certificate must be followed by a rolling restart,
// whatever triggered the regeneration.
//
// The proxy for "pods may hold this certificate" is a ready secret. A node
// secret deleted by hand and regenerated during a host change reports false,
// so pods keep serving the old certificate (signed by the same CA, without
// the new name) until something else restarts them.
func (rc *generateCert) generateNodeCert(
	ctx context.Context, log logr.Logger, cluster *resource.Cluster,
) (expiration string, replacedLiveCert bool, err error) {
	log.V(DEBUGLEVEL).Info("generating node certificate")

	// load the secret.  If it exists don't update the cert
	secret, err := resource.LoadTLSSecret(cluster.NodeTLSSecretName(),
		resource.NewKubeResource(ctx, rc.client, cluster.Namespace(), kube.DefaultPersister))
	if kube.IgnoreNotFound(err) != nil {
		return "", false, errors.Wrap(err, "failed to get node TLS secret")
	}

	var (
		regenerateCert bool
		SQLHost        string
	)

	if cluster.IsSQLIngressEnabled() && cluster.Status().SQLHost != cluster.Spec().Ingress.SQL.Host {
		SQLHost = cluster.Spec().Ingress.SQL.Host
		regenerateCert = true
	}

	// if the secret is ready then don't update the secret
	// the Actor should have already generated the secret
	if secret.Ready() {
		if regenerateCert {
			// Pods may already hold this certificate; the caller must roll them.
			replacedLiveCert = true
			log.V(DEBUGLEVEL).Info("regenerating node certificate because of change in SQLHost")
			if err = os.WriteFile(filepath.Join(rc.CertsDir, "ca.crt"), secret.CA(), 0644); err != nil {
				return "", false, errors.Wrap(err, "failed to write CA cert")
			}
		} else {
			log.V(DEBUGLEVEL).Info("not updating node certificate as it exists")
			exp, err := rc.getCertificateExpirationDate(ctx, log, secret.Key())
			return exp, false, err
		}
	}

	// hosts are the various DNS names and IP address that have to exist in the Node certificates
	// for the database to function
	hosts := []string{
		"localhost",
		"127.0.0.1",
		cluster.PublicServiceName(),
		fmt.Sprintf("%s.%s", cluster.PublicServiceName(), cluster.Namespace()),
		cluster.PublicServiceAddress(),
		fmt.Sprintf("*.%s", cluster.DiscoveryServiceName()),
		fmt.Sprintf("*.%s.%s", cluster.DiscoveryServiceName(), cluster.Namespace()),
		fmt.Sprintf("*.%s.%s.%s", cluster.DiscoveryServiceName(), cluster.Namespace(), cluster.Domain()),
	}

	if SQLHost != "" {
		hosts = append(hosts, SQLHost)
	}

	// create the Node Pair certificates
	err = errors.Wrap(
		security.CreateNodePair(
			rc.CertsDir,
			rc.CAKey,
			certificateLifetime,
			overwriteFiles,
			hosts),
		"failed to generate node certificate and key")

	if err != nil {
		return "", false, err
	}

	// Read the node certificates into memory
	ca, err := os.ReadFile(filepath.Join(rc.CertsDir, "ca.crt"))
	if err != nil {
		return "", false, errors.Wrap(err, "unable to read ca.crt")
	}

	pemCert, err := os.ReadFile(filepath.Join(rc.CertsDir, "node.crt"))
	if err != nil {
		return "", false, errors.Wrap(err, "unable to read node.crt")
	}

	pemKey, err := os.ReadFile(filepath.Join(rc.CertsDir, "node.key"))
	if err != nil {
		return "", false, errors.Wrap(err, "unable to ready node.key")
	}

	// TODO we are not using the TLS secret type, but are using Opaque secrets.
	// We should refactor and use the TLS secret type

	// create and save the TLS certificates into a secret
	secret = resource.CreateTLSSecret(cluster.NodeTLSSecretName(),
		resource.NewKubeResource(ctx, rc.client, cluster.Namespace(), kube.DefaultPersister))

	if err = secret.UpdateCertAndKeyAndCA(pemCert, pemKey, ca, log); err != nil {
		return "", false, errors.Wrap(err, "failed to update node TLS secret certs")
	}

	log.V(DEBUGLEVEL).Info("generated and saved node certificate and key")
	exp, err := rc.getCertificateExpirationDate(ctx, log, pemCert)
	return exp, replacedLiveCert, err
}

func (rc *generateCert) generateClientCert(ctx context.Context, log logr.Logger, cluster *resource.Cluster) error {
	log.V(DEBUGLEVEL).Info("generating client certificate")

	// load the secret.  If it exists don't update the cert
	secret, err := resource.LoadTLSSecret(cluster.ClientTLSSecretName(),
		resource.NewKubeResource(ctx, rc.client, cluster.Namespace(), kube.DefaultPersister))
	if client.IgnoreNotFound(err) != nil {
		return errors.Wrap(err, "failed to get client TLS secret")
	}

	// if the secret is ready then don't update the secret
	// the Actor should have already generated the secret
	//but we should read the expiration date
	if secret.Ready() {
		log.V(DEBUGLEVEL).Info("not updating client certificate")
		return nil
	}

	// Create the user for the certificate
	u := &security.SQLUsername{
		U: "root",
	}

	// Create the client certificates
	err = errors.Wrap(
		security.CreateClientPair(
			rc.CertsDir,
			rc.CAKey,
			certificateLifetime,
			overwriteFiles,
			*u,
			generatePKCS8Key),
		"failed to generate client certificate and key")
	if err != nil {
		return err
	}

	// Load the certificates into memory
	ca, err := os.ReadFile(filepath.Join(rc.CertsDir, "ca.crt"))
	if err != nil {
		return errors.Wrap(err, "unable to read ca.crt")
	}

	pemCert, err := os.ReadFile(filepath.Join(rc.CertsDir, "client.root.crt"))
	if err != nil {
		return errors.Wrap(err, "unable to read client.root.crt")
	}

	pemKey, err := os.ReadFile(filepath.Join(rc.CertsDir, "client.root.key"))
	if err != nil {
		return errors.Wrap(err, "unable to read client.root.key")
	}

	// create and save the TLS certificates into a secret
	secret = resource.CreateTLSSecret(cluster.ClientTLSSecretName(),
		resource.NewKubeResource(ctx, rc.client, cluster.Namespace(), kube.DefaultPersister))

	if err = secret.UpdateCertAndKeyAndCA(pemCert, pemKey, ca, log); err != nil {
		return errors.Wrap(err, "failed to update client TLS secret certs")
	}

	log.V(DEBUGLEVEL).Info("generated and saved client certificate and key")
	return nil
}

func (rc *generateCert) getCertificateExpirationDate(ctx context.Context, log logr.Logger, pemCert []byte) (string, error) {
	log.V(DEBUGLEVEL).Info("getExpirationDate from cert")
	block, _ := pem.Decode(pemCert)
	if block == nil {
		return "", errors.New("failed to decode certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse certificate")
	}

	log.V(DEBUGLEVEL).Info("getExpirationDate from cert", "Not before:", cert.NotBefore.Format(time.RFC3339), "Not after:", cert.NotAfter.Format(time.RFC3339))
	return cert.NotAfter.Format(time.RFC3339), nil
}
