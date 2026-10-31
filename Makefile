# Copyright 2026 The Cockroach Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

#
# This project requires the use of bazel.
# Install instuctions https://docs.bazel.build/versions/master/install.html

SHELL:=/usr/bin/env bash -O globstar

# Define path where we install binaries.
# NOTE(all): We need to use absolute paths because kubetest2 on go 1.19 does
# not support relative paths.
BINPATH := $(abspath bazel-bin)

# values used in workspace-status.sh
CLUSTER_NAME?=bazel-test
COCKROACH_DATABASE_VERSION=v21.2.3
DOCKER_IMAGE_REPOSITORY?=cockroachdb-operator
DOCKER_REGISTRY?=cockroachdb
GCP_PROJECT?=
GCP_ZONE?=
VERSION?=$(shell cat version.txt)

APP_VERSION?=v$(VERSION)
DEV_REGISTRY?=gcr.io/$(GCP_PROJECT)

# used for running e2e tests with OpenShift
PULL_SECRET?=
GCP_REGION?=
BASE_DOMAIN?=
EXTRA_KUBETEST2_PARAMS?=

#
# Unit Testing Targets
#
.PHONY: test/all
test/all: | test/apis test/pkg test/verify

.PHONY: test/apis
test/apis:
	bazel test //apis/... --test_arg=-test.v

.PHONY: test/pkg
test/pkg:
	bazel test //pkg/... --test_arg=-test.v

# This runs the all of the verify scripts and
# takes a bit of time.
.PHONY: test/verify
test/verify:
	bazel test --test_output=all //hack/...

.PHONY: test/lint
test/lint:
	bazel run //hack:verify-gofmt

# NODE_VERSION refers the to patch version of Kubernetes (e.g. 1.22.6)
.PHONY: test/smoketest
test/smoketest:
	@bazel run //hack/smoketest -- -dir $(PWD) -version $(NODE_VERSION)

# Run only e2e stort tests
# We can use this to only run one specific test
.PHONY: test/e2e-short
test/e2e-short:
	bazel test //e2e/... --test_arg=--test.short

# End to end testing targets
#
# k3d: use make test/e2e/k3d
# gke: use make test/e2e/gke
#
# kubetest2 binaries from the kubernetes testing team is used
# by the e2e tests.  We maintain the binaries and the binaries are
# downloaded from google storage by bazel.  See hack/bin/deps.bzl
# Once the repo releases binaries we should vendor the tag or
# download the built binaries.

# This target is used by kubetest2-tester-exec when running a k3d test
# It run k8s:k8s -type k3d which checks to see if k3d is up and running.
# Then bazel e2e testing is run.
# An example of calling this is using make test/e2e/testrunner-k3d-upgrades
test/e2e/testrunner-k3d-%: PACKAGE=$*
test/e2e/testrunner-k3d-%:
	bazel run //hack/k8s:k8s -- -type k3d
	bazel test --test_output=all --stamp //e2e/$(PACKAGE)/... --test_arg=-test.v --test_arg=-test.parallel=4 --test_arg=parallel=true

# Use this target to run e2e tests using a k3d k8s cluster.
# This target uses k3d to start a k8s cluster  and runs the e2e tests
# against that cluster.
#
# This is the main entrypoint for running the e2e tests on k3d.
# This target runs kubetest2 k3d that starts a k3d cluster
# Then kubetest2 tester exec is run which runs the make target
# test/e2e/testrunner-k3d.
# After the tests run the cluster is deleted.
# If you need a unique cluster name override CLUSTER_NAME.
test/e2e/k3d-%: PACKAGE=$*
test/e2e/k3d-%:
	bazel build //hack/bin/... //e2e/kubetest2-k3d/...
	PATH=$(BINPATH)/hack/bin:$(BINPATH)/e2e/kubetest2-k3d/kubetest2-k3d_/:${PATH} \
		kubetest2 k3d \
		--cluster-name=$(CLUSTER_NAME) --image rancher/k3s:v1.23.3-k3s1 --servers 3 \
		--up --down -v 10 --test=exec -- make test/e2e/testrunner-k3d-$(PACKAGE)

# This target is used by kubetest2-eks to run e2e tests.
.PHONY: test/e2e/testrunner-eks
test/e2e/testrunner-eks:
	KUBECONFIG=$(TMPDIR)/$(CLUSTER_NAME)-eks.kubeconfig.yaml $(BINPATH)/hack/bin/kubectl create -f hack/eks-storageclass.yaml
	bazel test --stamp //e2e/upgrades/...  --action_env=KUBECONFIG=$(TMPDIR)/$(CLUSTER_NAME)-eks.kubeconfig.yaml
	bazel test --stamp //e2e/create/...  --action_env=KUBECONFIG=$(TMPDIR)/$(CLUSTER_NAME)-eks.kubeconfig.yaml
	bazel test --stamp //e2e/decommission/...  --action_env=KUBECONFIG=$(TMPDIR)/$(CLUSTER_NAME)-eks.kubeconfig.yaml

# Use this target to run e2e tests with a eks cluster.
# This target uses kubetest2 to start a eks k8s cluster and runs the e2e tests
# against that cluster.
.PHONY: test/e2e/eks
test/e2e/eks:
	bazel build //hack/bin/... //e2e/kubetest2-eks/...
	PATH=${PATH}:$(BINPATH)/hack/bin:$(BINPATH)/e2e/kubetest2-eks/kubetest2-eks_/ \
	$(BINPATH)/hack/bin/kubetest2 eks --cluster-name=$(CLUSTER_NAME)  --up --down -v 10 \
		--test=exec -- make test/e2e/testrunner-eks

# This target is used by kubetest2-tester-exec when running a gke test
# k8s:k8s -type gke which checks to see if gke is up and running.
# Then bazel e2e testing is run.
# This target also installs the operator in the default namespace
# you may need to overrirde the DOCKER_IMAGE_REPOSITORY to match
# the GKEs project repo.
.PHONY: test/e2e/testrunner-gke
test/e2e/testrunner-gke:
	bazel run //hack/k8s:k8s -- -type gke
	K8S_CLUSTER=gke_$(GCP_PROJECT)_$(GCP_ZONE)_$(CLUSTER_NAME) \
	DEV_REGISTRY=$(DEV_REGISTRY) \
	# TODO this is not working because we create the cluster role binding now
	# for openshift.  We need to move this to a different target
	#bazel run --stamp --platforms=@io_bazel_rules_go//go/toolchain:linux_amd64 \
	#	//manifests:install_operator.apply
	bazel test --stamp //e2e/upgrades/...
	bazel test --stamp //e2e/create/...
	bazel test --stamp --test_arg=--pvc=true //e2e/pvcresize/...
	bazel test --stamp //e2e/decommission/...

# Use this target to run e2e tests with a gke cluster.
# This target uses kubetest2 to start a gke k8s cluster and runs the e2e tests
# against that cluster.
# This is the main entrypoint for running the e2e tests on gke.
# This target runs kubetest2 gke that starts a gke cluster
# Then kubetest2 tester exec is run which runs the make target
# test/e2e/testrunner-gke.
# After the tests run the cluster is deleted.
# If you need a unique cluster name override CLUSTER_NAME.
# You will probably want to override GCP_ZONE and GCP_PROJECT as well.
# The gcloud binary is used to start the cluster and is not installed by bazel.
# You also need gcp permission to start a cluster and upload containers to the
# projects registry.
.PHONY: test/e2e/gke
test/e2e/gke:
	bazel build //hack/bin/...
	PATH=${PATH}:$(BINPATH)/hack/bin kubetest2 gke --cluster-name=$(CLUSTER_NAME) \
		--zone=$(GCP_ZONE) --project=$(GCP_PROJECT) \
		--version latest --up --down -v 10 --ignore-gcp-ssh-key \
		--test=exec -- make test/e2e/testrunner-gke

.PHONY: test/e2e/testrunner-openshift
test/e2e/testrunner-openshift:
	bazel test --stamp //e2e/upgrades/...  --action_env=KUBECONFIG=$(HOME)/openshift-$(CLUSTER_NAME)/auth/kubeconfig
	bazel test --stamp //e2e/create/...  --action_env=KUBECONFIG=$(HOME)/openshift-$(CLUSTER_NAME)/auth/kubeconfig
	bazel test --stamp //e2e/decommission/...  --action_env=KUBECONFIG=$(HOME)/openshift-$(CLUSTER_NAME)/auth/kubeconfig

# Use this target to run e2e tests with a openshift cluster.
# This target uses kubetest2 to start a openshift cluster and runs the e2e tests
# against that cluster. A full TLD is required to creat an openshift clutser.
# This target runs kubetest2 openshift that starts a openshift cluster
# Then kubetest2 tester exec is run which runs the make target
# test/e2e/testrunner-openshift.  After the tests run the cluster is deleted.
# See the instructions in the kubetes2-openshift on running the
# provider.
.PHONY: test/e2e/openshift
test/e2e/openshift:
	bazel build //hack/bin/... //e2e/kubetest2-openshift/...
	PATH=${PATH}:$(BINPATH)/hack/bin:$(BINPATH)/e2e/kubetest2-openshift/kubetest2-openshift_/ \
	     kubetest2 openshift --cluster-name=$(CLUSTER_NAME) \
	     --gcp-project-id=$(GCP_PROJECT) \
	     --gcp-region=$(GCP_REGION) \
	     --base-domain=$(BASE_DOMAIN) \
	     --pull-secret-file=$(PULL_SECRET) \
	     $(EXTRA_KUBETEST2_PARAMS) \
	     --up --down --test=exec -- make test/e2e/testrunner-openshift

# This testrunner launchs the openshift packaging e2e test
# and requires an existing openshift cluster and the kubeconfig
# located in the usual place.
.PHONY: test/e2e/testrunner-openshift-packaging
test/e2e/testrunner-openshift-packaging: test/openshift-package
	bazel build //hack/bin:oc
	bazel test --stamp //e2e/openshift/... --cache_test_results=no \
		--action_env=KUBECONFIG=$(HOME)/openshift-$(CLUSTER_NAME)/auth/kubeconfig \
		--action_env=APP_VERSION=$(APP_VERSION) \
		--action_env=DOCKER_REGISTRY=$(DOCKER_REGISTRY)

# Run preflight checks for OpenShift. This expects a running OpenShift cluster.
# Eg. make test/preflight-<operator|bundle|marketplace>
test/preflight-%: CONTAINER=$*
test/preflight-%: release/generate-bundle
	@bazel run //hack:redhat-preflight -- $(CONTAINER)

#
# Different dev targets
#
.PHONY: dev/build
dev/build: dev/syncdeps
	bazel build //... --define APP_VERSION=$(APP_VERSION)

.PHONY: dev/fmt
dev/fmt:
	@echo +++ Running gofmt
	@bazel run //hack/bin:gofmt -- -s -w $(shell pwd)

.PHONY: dev/golangci-lint
dev/golangci-lint:
	@echo +++ Running golangci-lint
	@bazel run //hack/bin:golangci-lint run

.PHONY: dev/generate
dev/generate: | dev/update-codegen dev/update-crds

.PHONY: dev/update-codegen
dev/update-codegen:
	@bazel run //hack:update-codegen

# TODO: Be sure to update hack/verify-crds.sh if/when this changes
.PHONY: dev/update-crds
dev/update-crds:
	@bazel run //hack/bin:controller-gen \
		crd \
		rbac:roleName=role \
		webhook \
		paths=./... \
		output:crd:artifacts:config=config/crd/bases
	@hack/boilerplaterize hack/boilerplate/boilerplate.yaml.txt config/**/*.yaml

.PHONY: dev/syncbazel
dev/syncbazel:
	@bazel run //:gazelle -- fix -external=external -go_naming_convention go_default_library
	@bazel run //hack/bin:kazel -- --cfg-path hack/build/.kazelcfg.json

.PHONY: dev/syncdeps
dev/syncdeps:
	@go mod tidy
	@bazel run //:gazelle -- update-repos \
		-from_file=go.mod \
		-to_macro=hack/build/repos.bzl%_go_dependencies \
		-build_file_generation=on \
		-build_file_proto_mode=disable \
		-prune
	@make dev/syncbazel

.PHONY: dev/up
dev/up: dev/down
	@hack/dev.sh up

.PHONY: dev/down
dev/down:
	@bazel build //hack/bin:k3d
	@hack/dev.sh down
#
# Targets that allow to install the operator on an existing cluster
#
.PHONY: k8s/apply
k8s/apply:
	DEV_REGISTRY=$(DEV_REGISTRY) \
	bazel run --stamp --platforms=@io_bazel_rules_go//go/toolchain:linux_amd64 \
		//config/default:install.apply \
		--define APP_VERSION=$(APP_VERSION)

.PHONY: k8s/delete
k8s/delete:
	DEV_REGISTRY=$(DEV_REGISTRY) \
	bazel run --stamp --incompatible_use_cc_configure_from_rules_cc \
		//config/default:install.delete \
		--define APP_VERSION=$(APP_VERSION)

#
# Release targets
#

# This target sets the version in version.txt, creates a new branch for the
# release, and generates all of the files required to cut a new release.
.PHONY: release/new
release/new:
	# TODO: verify clean, up to date master branch...
	@bazel run //hack/release -- -dir $(PWD) -version $(VERSION)

# Generate various config files, which usually contain the current operator
# version, latest CRDB version, a list of supported CRDB versions, etc.
#
# This also generates install/crds.yaml and install/operator.yaml which are
# pre-built kustomize bases used in our docs.
# Set CRDB_VERSION_UPDATE_ARGS=-allow-removals when intentionally removing a
# previously supported CockroachDB version.
.PHONY: release/gen-templates
release/gen-templates:
	bazel run //hack/update_crdb_versions -- $(CRDB_VERSION_UPDATE_ARGS)
	@hack/boilerplaterize hack/boilerplate/boilerplate.yaml.txt $(PWD)/crdb-versions.yaml
	bazel run //hack/crdbversions:crdbversions -- -operator-version $(APP_VERSION) -crdb-versions $(PWD)/crdb-versions.yaml -repo-root $(PWD)
	bazel run //config/crd:manifest.preview > install/crds.yaml
	bazel run //config/operator:manifest.preview > install/operator.yaml
	bazel run //config/user_permissions:manifest.preview > install/user_permission_clusterrole.yaml

# Generate various manifest files for OpenShift. We run this target after the
# operator version is changed. The results are committed to Git.
.PHONY: release/gen-files
release/gen-files: | release/gen-templates dev/generate
	git add . && git commit -m "Bump version to $(VERSION)"

.PHONY: release/image
release/image:
	# TODO this bazel clean is here because we need to pull the latest image from redhat registry every time
	# but this removes all caching and makes compile time for developers LONG.
	bazel clean --expunge
	DOCKER_REGISTRY=$(DOCKER_REGISTRY) \
	DOCKER_IMAGE_REPOSITORY=$(DOCKER_IMAGE_REPOSITORY) \
	APP_VERSION=$(APP_VERSION) \
	bazel run --stamp \
		//:push_operator_image

#
# RedHat OpenShift targets
#

#REDHAT IMAGE BUNDLE
RH_BUNDLE_REGISTRY?=registry.connect.redhat.com/cockroachdb
RH_BUNDLE_IMAGE_REPOSITORY?=cockroachdb-operator-bundle
RH_BUNDLE_VERSION?=$(VERSION)
RH_COCKROACH_DATABASE_IMAGE=registry.connect.redhat.com/cockroachdb/cockroach:$(COCKROACH_DATABASE_VERSION)
RH_OPERATOR_IMAGE?=registry.connect.redhat.com/cockroachdb/cockroachdb-operator:$(APP_VERSION)

# Generate package bundles.
# Default options for channels if not pre-specified.
CHANNELS?=stable
DEFAULT_CHANNEL?=stable

ifneq ($(origin CHANNELS), undefined)
PKG_CHANNELS := --channels=$(CHANNELS)
endif
ifneq ($(origin DEFAULT_CHANNEL), undefined)
PKG_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)
endif
PKG_MAN_OPTS ?= "$(PKG_CHANNELS) $(PKG_DEFAULT_CHANNEL)"

# Build the packagemanifests
.PHONY: release/generate-bundle
release/generate-bundle:
	bazel run //hack:bundle -- $(RH_BUNDLE_VERSION) $(RH_OPERATOR_IMAGE) $(PKG_MAN_OPTS) $(RH_COCKROACH_DATABASE_IMAGE)

.PHONY: release/publish-operator
publish-operator:
	./build/release/teamcity-publish-release.sh

.PHONY: release/publish-operator-openshift
publish-operator-openshift:
	./build/release/teamcity-publish-openshift.sh

.PHONY: release/publish-openshift-bundle
release/publish-openshift-bundle:
	./build/release/teamcity-publish-openshift-bundle.sh
