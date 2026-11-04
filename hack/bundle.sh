#!/usr/bin/env bash

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
set -euo pipefail

# must be run with bazel
if [[ -z "${BUILD_WORKSPACE_DIRECTORY:-}" ]]; then
  echo 'Must be run via "make release/generate-bundle"' >&2
  exit 1
fi

# Catalog targets for this release. Complete the platform checks in README.md
# before publishing these bundles. The Marketplace catalog is removed in OCP 4.22.
openshift_versions="v4.12-v4.22"
marketplace_openshift_versions="v4.12-v4.21"

# ensure tool dependencies are on the path
export PATH="$(pwd)/hack/bin:${PATH}"

main() {
  local rh_bundle_version="${1}"
  local rh_operator_image="${2}"
  local rh_package_options="${3}"
  local rh_crdb_image="${4}"
  local deploy_path="bundle"

  # move to the root project directory
  cd "${BUILD_WORKSPACE_DIRECTORY}"

  echo "+++ Generating bundle with the following settings"
  echo "RH_BUNDLE_VERSION=${rh_bundle_version}"
  echo "RH_OPERATOR_IMAGE=${rh_operator_image}"
  echo "RH_PACKAGE_OPTS=${rh_package_options}"
  echo "RH_CRDB_IMAGE=${rh_crdb_image}"
  echo "DEPLOY_PATH=${deploy_path}"

  mkdir -p "${deploy_path}"

  # ensure operator image is pinned to a SHA (OpenShift requirement)
  docker pull "${rh_operator_image}"
  rh_operator_image="$(docker inspect --format='{{index .RepoDigests 0}}' "${rh_operator_image}")"

  generate_manifests
  for pkg in cockroachdb-certified cockroachdb-certified-rhmp; do
    generate_bundle "${rh_bundle_version}" "${rh_package_options}" "${deploy_path}/${pkg}" "${rh_operator_image}"
  done

  patch_marketplace_csv "${deploy_path}" cockroachdb-certified-rhmp
}

generate_manifests() {
  # Generate CSV in config/manifests and add boilerplate back (lost when regenerating)
  operator-sdk generate kustomize manifests -q --apis-dir apis
  hack/boilerplaterize hack/boilerplate/boilerplate.yaml.txt config/manifests/**/*.yaml
}

generate_bundle() {
  local version="${1}"
  local opts="${2}"
  local dir="${3}"
  local pkg="$(basename ${dir})"
  local img="${4}"

  # Generate the new package bundle
  kustomize build config/manifests | operator-sdk generate bundle -q \
  --version "${version}" \
  ${opts} \
  --output-dir "${dir}"

  # Ensure package name is correct for the specific bundle and that the CSV name matches the package name. Also removing
  # the testing annotations since these are handled automatically upstream. Also add openshift version annotation
  local supported_versions="${openshift_versions}"
  if [[ "${pkg}" == cockroachdb-certified-rhmp ]]; then
    supported_versions="${marketplace_openshift_versions}"
  fi
  local openshift_version_annotation="  com.redhat.openshift.versions: ${supported_versions}"
  sed \
  -e "s+annotations:+annotations:\n  # OpenShift catalog version range\n${openshift_version_annotation}+" \
  -e "s/package.v1: cockroach-operator/package.v1: ${pkg}/g" \
  -e "/\s*# Annotations for testing/d" \
  -e "/\s*operators.operatorframework.io.test/d" \
  "${dir}/metadata/annotations.yaml" > "${dir}/metadata/annotations.yaml.new"

  mv "${dir}/metadata/annotations.yaml.new" "${dir}/metadata/annotations.yaml"

  # Update CSV with correct images, timestamps and upgrade range
  adapt_csv "${dir}" "${img}" "${version}"

  # move the dockerfile into the bundle directory and make it valid
  sed \
  -e "/\s*tests\/scorecard/d" \
  -e "s+${dir}/++g" \
  bundle.Dockerfile > "${dir}/Dockerfile"

  rm bundle.Dockerfile
}

adapt_csv() {
  local dir="${1}"
  local img="${2}"
  local version="${3}"
  local pkg="$(basename ${dir})"
  local csv="${dir}/manifests/${pkg}.clusterserviceversion.yaml"

  # rename csv to match package name
  mv "${dir}/manifests/cockroach-operator.clusterserviceversion.yaml" "${csv}"

  # replace RH_COCKROACH_OP_IMAGE_PLACEHOLDER with the proper image and CREATED_AT_PLACEHOLDER with the current time.
  # The CSVs don't set spec.replaces, so olm.skipRange is what lets OLM offer this version to installed 2.x operators.
  # The ^ and $ anchors keep the match to the top-level metadata annotations.
  sed \
  -e "s+RH_COCKROACH_OP_IMAGE_PLACEHOLDER+${img}+g" \
  -e "s+CREATED_AT_PLACEHOLDER+"$(date +"%FT%H:%M:%SZ")"+g" \
  -e "s+^  annotations:\$+  annotations:\n    olm.skipRange: '>=2.0.0 <${version}'+" \
  "${csv}" > "${csv}.new"

  mv "${csv}.new" "${csv}"
}

patch_marketplace_csv() {
  # extra annotations that are needed for the marketplace CSV
  local rhmp_annotations="    marketplace.openshift.io/remote-workflow: https://marketplace.redhat.com/en-us/operators/${2}/pricing?utm_source=openshift_console"
  rhmp_annotations="${rhmp_annotations}\n    marketplace.openshift.io/support-workflow: https://marketplace.redhat.com/en-us/operators/${2}/support?utm_source=openshift_console"

  local csv="${1}/cockroachdb-certified-rhmp/manifests/${2}.clusterserviceversion.yaml"
  sed "s+  annotations:+  annotations:\n${rhmp_annotations}+" \
  "${csv}" > "${csv}.new"
  mv "${csv}.new" "${csv}"
}

main "$@"
