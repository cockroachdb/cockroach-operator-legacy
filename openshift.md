# OpenShift

For OLM installations, the first section covers operator updates, database image
overrides, and platform versions. The rest of this guide is for contributors testing
the operator on a dedicated OpenShift cluster, from the root of this repository.

Steps in this guide:

* Using OLM
* Prerequisites
* Create an OpenShift cluster with GCP (~40m)
* Installing the operator
* Creating a CrdbCluster
* Verifying the cluster is operational
* Tearing down the cluster

## Using OLM

An OLM bundle contains the CSV, operator Deployment, CRD, permissions, and database
image references. Red Hat publishes bundles into OpenShift catalogs. A Subscription selects
updates from a catalog and channel; OLM applies them through an InstallPlan.

The operator image and bundles are released separately. For publishing instructions,
see [Publish OpenShift bundles](README.md#publish-openshift-bundles).

### Platform versions

The v2.18.6 release prepares these catalog ranges:

| Package | OpenShift catalog range |
| --- | --- |
| `cockroachdb-certified` | 4.12-4.22 |
| `cockroachdb-certified-rhmp` | 4.12-4.21 |

| OpenShift | Kubernetes minor |
| --- | --- |
| 4.19 | 1.32 |
| 4.20 | 1.33 |
| 4.21 | 1.34 |
| 4.22 | 1.35 |

These are release targets until testing and certification are complete. The CSV's
`minKubeVersion` is a minimum, not a tested maximum. Confirm that the published
release is available and supported on your platform before installing it.

OpenShift 4.22 removes the Marketplace catalog. Use the certified package for new
installations there. Existing Marketplace installations need a supported package
transition; changing the Subscription package name alone is not a normal update.
See [Red Hat's catalog guidance](https://access.redhat.com/solutions/7137196).

### Update the operator

1. Record the platform version, installed CSV, and Subscription package, channel, and
   CatalogSource. Confirm the new bundle is available for that OpenShift version.

   ```shell
   oc version -o yaml
   oc -n <operator-namespace> get subscription <subscription-name> -o yaml
   oc -n <operator-namespace> get csv <installed-csv> -o yaml
   ```

2. Keep the database version and existing image overrides fixed during the operator
   update. For controlled approval, set `installPlanApproval: Manual` and ensure any
   GitOps approval automation follows the same policy.
3. Read `status.installPlanRef` from the Subscription and review that plan's target
   CSVs and resource changes. The path may include intermediate operator versions.
   `startingCSV` selects an initial installation; it does not request or pin an upgrade.

   ```shell
   oc -n <operator-namespace> get installplan <referenced-plan> -o yaml
   ```

4. Approve the intended plan. Wait for the InstallPlan to complete, the installed CSV
   to reach `Succeeded`, and the operator Deployment to become available. Verify the
   running operator image and Subscription status, then check database pods and SQL
   health. Reconciliation can roll database pods if their configuration has drifted.

### Use a database image not bundled with the installed operator

Use a certified image for the intended database version and CPU architecture. Confirm
that its digest is available through your registry or mirror. An image override does
not update the operator or change its platform compatibility.

1. Add the mapping to `Subscription.spec.config.env`, preserving existing entries.
   With GitOps, make this change in the source manifest. Replace the placeholders below:

   ```yaml
   spec:
     config:
       env:
         - name: RELATED_IMAGE_COCKROACH_v<major>_<minor>_<patch>
           value: <registry>/<repository>@sha256:<digest>
   ```

   For example, `v25.4.19` uses the variable name `RELATED_IMAGE_COCKROACH_v25_4_19`.
   OLM manages the CSV and Deployment; keep this configuration in the Subscription.
2. Allow for an operator restart, which can reconcile existing database configuration
   and roll pods. Wait for the Deployment rollout and verify the mapping in the
   running operator pod before changing the database CR.
3. Follow the database upgrade procedure, setting `spec.cockroachDBVersion` to the
   intended version. `spec.image.name` takes precedence, so leave it unset for this
   route. Check pod images, readiness, SQL health, and application behavior afterward.
4. Keep each mapping while any managed database still uses it. Remove an override only
   after confirming the operator supplies the required image mapping itself.

Review the [release notes](CHANGELOG.md) before a database upgrade, including the
v2.18.6 note about the first version change on existing clusters. Database major
upgrades also require their own finalization and rollback procedure.

### Before upgrading OpenShift

Confirm the installed operator is supported on the destination platform and its
package is available in the destination catalog or mirror. Upgrade OpenShift as a
separate change, keeping the database version fixed. Afterward, check catalog readiness,
Subscription resolution, operator availability, database pods, and SQL health.

For migration to the Helm-installed CockroachDB Operator, use the separate
[migration guide](https://www.cockroachlabs.com/docs/v26.2/migrate-cockroachdb-kubernetes-operator).

## Prerequisites

For the rest of this guide, we'll be using some placeholders:

* `<CLUSTER_NAME>` - The name of the OpenShift cluster
* `<DNS_ZONE>` - The DNS zone that will be used by the installer script
* `<GCP_PROJECT>` - Your GCP project id (not necessarily the same as the name)
* `<GCP_REGION>` - The region you want to deploy to (e.g. `us-central1`, `us-east1`)

You can get your project id by running the following command:

```shell
gcloud projects list --format="value(PROJECT_ID)" --filter="PROJECT_ID ~ ^<START_OF_PROJECT_NAME>"
```

> Replace <START_OF_PROJECT_NAME> with your project. E.g. muto-playground)

### Binaries

You'll need to have a few binaries available on the path. Specifically:

* gcloud
* oc
* openshift-install

`gcloud` will need to be installed via the instructions [here](https://cloud.google.com/sdk/docs/install). However `oc`
and `openshift-install` can be installed with `bazel build //hack/bin/...`.

### Ensuring GCP is configured correctly

In order to create the OpenShift in GCP, you'll need to have a DNS configured. You can verify that it's setup correctly
by running `gcloud dns managed-zones list`. If you have an existing domain name sitting somewhere that you'd like to
migrate over, check out [this guide](https://cloud.google.com/dns/docs/migrating).

You should see a publicly visible DNS NAME entry for a root domain like `example.com.`. Take note of this value. We'll
need it in the next step (referred to as `<DNS_ZONE>` hereafter).

### Environment setup

The installer script we use here will use the default configuration from gcloud. For this reason you'll need to ensure
you've logged into and set the default context.

```shell
gcloud auth login application-default   # ensure you're logged in
gcloud config set project <GCP_PROJECT> # set the default project
```

You'll want the following environment variables to be exported (by running these in your terminal):

```shell
export PATH="$(pwd)/bazel-bin/hack/bin:${PATH}" # ensures oc and openshift-install are on the PATH
export CLUSTER_NAME="<CLUSTER_NAME>"
export DNS_ZONE="<DNS_ZONE>"
export GCP_PROJECT="<GCP_PROJECT>"
export GCP_REGION="<GCP_REGION>"
```

## Create an OpenShift cluster

### Setup a new cluster in OpenShift

1. Login to [OpenShift](https://cloud.redhat.com/openshift/)
2. Press "Create Cluster" button
3. Scroll down to "Run it yourself"
4. Click on Google Cloud
5. Select Installer Provisioned (recommended)
6. Click on download pull secret

> NOTE: You must download the pull secret file as it is required for the install. This guide assumes it's at
`${HOME}/Downloads/pull-secret.txt`.


### Running the installer

Next, let's run the installer script. This will create a cluster in GCP and install OpenShift on it.

```shell
hack/openshift-gcp-create.sh \
  -p "${GCP_PROJECT}" \
  -s <pull secret file> \
  -z "${DNS_ZONE}" \
  -n "${CLUSTER_NAME}" \
  -r "${GCP_REGION}"
```

This will take some time. Be sure to take note of the web console details (URL, username, and password). You'll need
this later. This info will be printed at the end of the run.

Verify that the install worked by running the following commands:

```shell
export KUBECONFIG="${HOME}/openshift-${CLUSTER_NAME}/auth/kubeconfig"
oc whoami
```

> NOTE: DO NOT delete the folder where the installation was made, in our case `${HOME}/openshift-<cluster name>`. If you
> delete this you will have to decommission manually the infrastructure from GCP.

## Installing the operator

You have a few choices when in comes to installing the CRDs and the operator.

### From the repo

This option will install the CRDs and the operator as they are defined locally. This will typically mean using a
published image (e.g. `cockroachdb/cockroach-operator:v2.2.1`)

```shell
oc apply -f install/crds.yaml
oc apply -f install/operator.yaml
oc wait --for condition=Available deploy/cockroach-operator --timeout=2m
```

> NOTE: If you'd rather pull from GitHub, you can prefix the paths with
`https://raw.githubusercontent.com/cockroachdb/cockroach-operator/<tag>/`

### From OperatorHub

You can also install the operator from OperatorHub. This option will install the latest published version of the
operator.

* Login to the web console using the address, username, and password you got from the installer script output.
* From the side menu, select Operators -> OperatorHub
* Search for cockroach and install the one _**NOT**_ marked with the Marketplace tag

## Creating a CrdbCluster

With the operator running we can now create CrdbCluster resources. For example, we can deploy the example cluster by
running the following:

```shell
oc apply -f examples/example.yaml
oc rollout status -w sts/cockroachdb
```

## Verifying the cluster is operational

### Deploy the client pod

With our cluster in place and running, we can test it out by running some basic commands or a Crdb workload. To do this
we'll need to deploy a pod that can access the cluster. We have one predefined in the examples directory.

```shell
oc apply -f examples/client-secure-operator.yaml
oc wait --for condition=Ready pod/cockroachdb-client-secure --timeout=2m
```

### Running some commands

When we want to run commands (e.g. `sql` or `workload`) we do this by running those commands via the secure client pod.

```shell
# general format
oc exec -it cockroachdb-client-secure -- cockroach <ARGS>

# EXAMPLE: execute some SQL
oc exec -it cockroachdb-client-secure -- cockroach sql --execute "SHOW DATABASES;"

# EXAMPLE: run a workflow
oc exec -it cockroachdb-client-secure -- cockroach workload init bank
oc exec -it cockroachdb-client-secure -- cockroach workload run bank --duration 1m
```

## Tearing down the cluster

The last step is to tear it all down:

```shell
# delete the cluster
GOOGLE_CREDENTIALS=~/.gcp/osServiceAccount.json \
  openshift-install destroy cluster --dir "${HOME}/openshift-${CLUSTER_NAME}" --log-level=debug

# delete the service accounts
hack/openshift-iam-delete.sh -p "${GCP_PROJECT}" -n "${CLUSTER_NAME}"
```

`openshift-install` will take care of removing the local directory in "${HOME}". However, you will need to archive the
cluster from [OpenShift](https://cloud.redhat.com/openshift/).
