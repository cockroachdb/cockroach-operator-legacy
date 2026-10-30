# Copyright 2019 The Jetstack cert-manager contributors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

load("@bazel_tools//tools/build_defs/repo:http.bzl", "http_file", "http_archive")
load("@bazel_gazelle//:deps.bzl", "go_repository")

# This controls the version for all openshift binaries (opm, oc, opernshift-install, etc.)
OPENSHIFT_VERSION = "4.10.18"
OPENSHIFT_REPO = "https://mirror.openshift.com/pub/openshift-v4/x86_64/clients/ocp/{}".format(OPENSHIFT_VERSION)

# filenames and versions from ${OPENSHIFT_REPO}/sha256sum.txt
OPENSHIFT_BINS = {
    "preflight": {
        # currently, preflight is only available on linux
        "preflight_linux": {
            "url": "https://github.com/redhat-openshift-ecosystem/openshift-preflight/releases/download/1.20.0/preflight-linux-amd64",
            "sha": "43a8c5046c800442e58a37f583a9523d79421e81aec3a741c53c9fcffc82955b",
        },
    },
    "oc": {
        "oc_darwin": {
            "url": "{}/openshift-client-mac-{}.tar.gz".format(OPENSHIFT_REPO, OPENSHIFT_VERSION),
            "sha": "285c307491d8ffd19c065a942515fda78e53f95289d4b4985aa4c92439f7f339",
        },
        "oc_linux": {
            "url": "{}/openshift-client-linux-{}.tar.gz".format(OPENSHIFT_REPO, OPENSHIFT_VERSION),
            "sha": "101bc7e11604b829157b3b314de3760eec857e55f51eeca978825307ff61c190",
        },
    },
    "openshift-install": {
        "openshift_darwin": {
            "url": "{}/openshift-install-mac-{}.tar.gz".format(OPENSHIFT_REPO, OPENSHIFT_VERSION),
            "sha": "3a36acb92a6759d964a1af62512c747e075a2937a6368203d0598d804db10da2",
         },
        "openshift_linux": {
            "url": "{}/openshift-install-linux-{}.tar.gz".format(OPENSHIFT_REPO, OPENSHIFT_VERSION),
            "sha": "27e6ccb60ce2c7dfe611e1639642277572af78a21c622a7443d5a19006b2e45b",
        },
    },
    "opm": {
        "opm_darwin": {
            "url": "{}/opm-mac-{}.tar.gz".format(OPENSHIFT_REPO, OPENSHIFT_VERSION),
            "sha": "36d7104b1fd29e77a880b63e3e1aa67639a48cca1fdf537411b40a0c36140dba",
            "src": "darwin-amd64-opm",
         },
        "opm_linux": {
            "url": "{}/opm-linux-{}.tar.gz".format(OPENSHIFT_REPO, OPENSHIFT_VERSION),
            "sha": "6d422682fd688cbebc7818247005e2baf87675efef4931d2f0a2e744dc613b88",
            "src": "opm",
        },
    },
}

def install():
    install_misc()
    install_integration_test_dependencies()

    install_crdb()
    install_golangci_lint()
    install_kubectl()
    install_k3d()
    install_kubetest2()
    install_kubetest2_aws()
    install_kubetest2_exe()
    install_kubetest2_gke()
    install_kustomize()
    install_oc()
    install_operator_sdk()
    install_opm()
    install_openshift()
    install_preflight()

    # Install golang.org/x/build as kubernetes/repo-infra requires it for the
    # build-tar bazel target.
    go_repository(
        name = "org_golang_x_build",
        build_file_generation = "on",
        build_file_proto_mode = "disable",
        importpath = "golang.org/x/build",
        sum = "h1:hXVePvSFG7tPGX4Pwk1d10ePFfoTCc0QmISfpKOHsS8=",
        version = "v0.0.0-20190927031335-2835ba2e683f",
    )

def install_misc():
    http_file(
        name = "jq_linux",
        executable = 1,
        sha256 = "af986793a515d500ab2d35f8d2aecd656e764504b789b66d7e1a0b727a124c44",
        urls = ["https://github.com/stedolan/jq/releases/download/jq-1.6/jq-linux64"],
    )

    http_file(
        name = "jq_osx",
        executable = 1,
        sha256 = "5c0a0a3ea600f302ee458b30317425dd9632d1ad8882259fcaf4e9b868b2b1ef",
        urls = ["https://github.com/stedolan/jq/releases/download/jq-1.6/jq-osx-amd64"],
    )

    http_file(
        #this tool is used on open shift generation to generate the csv
        name = "faq_linux",
        executable = 1,
        sha256 = "6c9234d0b2b024bf0e7c845fc092339b51b94e5addeee9612a7219cfd2a7b731",
        urls = ["https://github.com/jzelinskie/faq/releases/download/0.0.7/faq-linux-amd64"],
    )

    http_file(
        name = "faq_osx",
        executable = 1,
        sha256 = "869f4d8acaa1feb11ce76b2204c5476b8a04d9451216adde6b18e2ef2f978794",
        urls = ["https://github.com/jzelinskie/faq/releases/download/0.0.7/faq-darwin-amd64"],
    )

# Install dependencies used by the controller-runtime integration test framework
def install_integration_test_dependencies():
    http_file(
        name = "kube-apiserver_darwin_arm64",
        executable = 1,
        sha256 = "b575b687097b4662b7eb2ea5453e4b381994ddf34b85ca4bb8e4f16c5f86bedb",
        urls = ["https://storage.googleapis.com/cockroach-operator-testing-assets/kube-apiserver-1.24.2_darwin_arm64"],
    )

    http_file(
        name = "kube-apiserver_darwin_amd64",
        executable = 1,
        sha256 = "800eedc293bcc72eb962df3e8006d7106790ca583a4ea5c107453bab3bf859b4",
        urls = ["https://storage.googleapis.com/cockroach-operator-testing-assets/kube-apiserver-1.24.2_darwin_amd64"],
    )

    http_file(
        name = "kube-apiserver_linux_amd64",
        executable = 1,
        sha256 = "a70a704835450130f8feeac9d42617d527e269c25a5aa92590e54533c2d4f776",
        urls = ["https://storage.googleapis.com/cockroach-operator-testing-assets/kube-apiserver-1.24.2_linux_amd64"],
    )

    http_archive(
        name = "com_coreos_etcd_darwin_amd64",
        sha256 = "27245adea2e0951913276d8c321d79b91caaf904ae3fdaab65194ab41c01db08",
        urls = ["https://github.com/etcd-io/etcd/releases/download/v3.4.16/etcd-v3.4.16-darwin-amd64.zip"],
        build_file_content = """
filegroup(
    name = "file",
    srcs = [
        "etcd-v3.4.16-darwin-amd64/etcd",
    ],
    visibility = ["//visibility:public"],
)
""",
    )

    http_archive(
        name = "com_coreos_etcd_linux_amd64",
        sha256 = "2e2d5b3572e077e7641193ed07b4929b0eaf0dc2f9463e9b677765528acafb89",
        urls = ["https://github.com/etcd-io/etcd/releases/download/v3.4.16/etcd-v3.4.16-linux-amd64.tar.gz"],
        build_file_content = """
filegroup(
    name = "file",
    srcs = [
        "etcd-v3.4.16-linux-amd64/etcd",
    ],
    visibility = ["//visibility:public"],
)
""",
    )

# Define rules for different kubectl versions
def install_kubectl():
    http_file(
        name = "kubectl_1_18_darwin",
        executable = 1,
        sha256 = "5eda86058a3db112821761b32afce3fdd2f6963ab580b1780a638ac323864eba",
        urls = ["https://storage.googleapis.com/kubernetes-release/release/v1.18.0/bin/darwin/amd64/kubectl"],
    )

    http_file(
        name = "kubectl_1_18_linux",
        executable = 1,
        sha256 = "bb16739fcad964c197752200ff89d89aad7b118cb1de5725dc53fe924c40e3f7",
        urls = ["https://storage.googleapis.com/kubernetes-release/release/v1.18.0/bin/linux/amd64/kubectl"],
    )

def install_k3d():
    versions = {
        "k3d_darwin": {
            "url": "https://github.com/k3d-io/k3d/releases/download/v5.8.3/k3d-darwin-amd64",
            "sha": "fd0f8e9e8ea4d8bc3674572ca6ed0833b639bf57c43c708616d937377324cfea",
        },
        "k3d_linux": {
            "url": "https://github.com/k3d-io/k3d/releases/download/v5.8.3/k3d-linux-amd64",
            "sha": "dbaa79a76ace7f4ca230a1ff41dc7d8a5036a8ad0309e9c54f9bf3836dbe853e",
        },
        "k3d_m1": {
            "url": "https://github.com/k3d-io/k3d/releases/download/v5.8.3/k3d-darwin-arm64",
            "sha": "8da468daa7dc7cf7cdd4735f90a9bb05179fa27858250f62e3d8cdf5b5ca0698",
        }
    }

    for k, v in versions.items():
      http_file(
          name = k,
          executable = 1,
          sha256 = v["sha"],
          urls = [v["url"]],
      )

# Define rules for different golangci-lint versions
def install_golangci_lint():
    http_archive(
        name = "golangci_lint_darwin",
        sha256 = "f6f06d94b6241521c53d15450c5209b028270bf966f842afb11c030c79f5bc16",
        urls = ["https://github.com/golangci/golangci-lint/releases/download/v2.12.2/golangci-lint-2.12.2-darwin-amd64.tar.gz"],
        build_file_content =
         """
filegroup(
     name = "file",
     srcs = [
        "golangci-lint-2.12.2-darwin-amd64/golangci-lint",
     ],
     visibility = ["//visibility:public"],
)
    """,
    )

    http_archive(
            name = "golangci_lint_m1",
            sha256 = "a9c54498731b3128f79e090be6110f3e5fffccc617b08142ed244d4126c73f29",
            urls = ["https://github.com/golangci/golangci-lint/releases/download/v2.12.2/golangci-lint-2.12.2-darwin-arm64.tar.gz"],
            build_file_content =
             """
filegroup(
    name = "file",
    srcs = [
       "golangci-lint-2.12.2-darwin-arm64/golangci-lint",
    ],
    visibility = ["//visibility:public"],
)
    """,
    )

    http_archive(
        name = "golangci_lint_linux",
        sha256 = "8df580d2670fed8fa984aac0507099af8df275e665215f5c7a2ae3943893a553",
        urls = ["https://github.com/golangci/golangci-lint/releases/download/v2.12.2/golangci-lint-2.12.2-linux-amd64.tar.gz"],
        build_file_content =
         """
filegroup(
     name = "file",
     srcs = [
        "golangci-lint-2.12.2-linux-amd64/golangci-lint",
     ],
     visibility = ["//visibility:public"],
)
    """,
    )

# Define rules for different oc versions
def install_oc():
    versions = OPENSHIFT_BINS["oc"]

    for k, v in versions.items():
        http_archive(
            name = k,
            sha256 = v["sha"],
            urls = [v["url"]],
            build_file_content =
             """
filegroup(
     name = "file",
     srcs = ["oc"],
     visibility = ["//visibility:public"],
)
    """,
        )

## Fetch kubetest2 binary used during e2e tests
def install_kubetest2():
    # install kubetest2 binary
    http_file(
        name = "kubetest2_darwin",
        executable = 1,
        sha256 = "9fab82888e5c955778a8c49fdd2b9d2216be1a58f70615977fb92f678383e688",
        urls = ["https://storage.googleapis.com/cockroach-operator-bazel-artifacts/kubetest2_darwin_amd64_v1/kubetest2"],
    )

    http_file(
        name = "kubetest2_linux",
        executable = 1,
        sha256 = "f9306a103dc222d51753e788550bd77c05a910a957a7eb4901ccb7f78256f7b8",
        urls = ["https://storage.googleapis.com/cockroach-operator-bazel-artifacts/kubetest2_linux_amd64_v1/kubetest2"],
    )

## Fetch kubetest2-gke binary used during e2e tests
def install_kubetest2_gke():
    # install kubetest2-gke binary
    # TODO osx support
    http_file(
        name = "kubetest2_gke_darwin",
        executable = 1,
        sha256 = "12d0b7cc9eb2ab2befe781f08672f2707631debd852f2805bed1565699e44a6e",
        urls = ["https://storage.googleapis.com/cockroach-operator-bazel-artifacts/kubetest2-gke_darwin_amd64_v1/kubetest2-gke"],
    )

    http_file(
        name = "kubetest2_gke_linux",
        executable = 1,
        sha256 = "2b294abe037243e8bf71fcef6f02d93ee69abadfd0034681237478fa69474097",
        urls = ["https://storage.googleapis.com/cockroach-operator-bazel-artifacts/kubetest2-gke_linux_amd64_v1/kubetest2-gke"],
    )

## Fetch kubetest2-tester-exe binary used during e2e tests
def install_kubetest2_exe():
    # install kubetest2-exe binary
    # TODO osx support
    http_file(
        name = "kubetest2_exe_darwin",
        executable = 1,
        sha256 = "15a6c8ff2e6b3962954553eacc9aeefb40ac81f67c326144db2ad94d58756357",
        urls = ["https://storage.googleapis.com/cockroach-operator-bazel-artifacts/kubetest2-tester-exec_darwin_amd64_v1/kubetest2-tester-exec"],
    )

    http_file(
        name = "kubetest2_exe_linux",
        executable = 1,
        sha256 = "b96c9b651c6a4449adfa41d6760ec1b34ec02230b5debd850976ced3926d80db",
        urls = ["https://storage.googleapis.com/cockroach-operator-bazel-artifacts/kubetest2-tester-exec_linux_amd64_v1/kubetest2-tester-exec"],
    )

## Fetch operator-sdk used on generating csv
def install_operator_sdk():
    versions = {
        "operator_sdk_darwin": {
            "file": "operator-sdk_darwin_amd64",
            "sha": "ca3e4028cd62f21f4ed988907b884be530098e7c40523e89046dd8c5b0178eb9",
        },
        "operator_sdk_linux": {
            "file": "operator-sdk_linux_amd64",
            "sha": "20da1fcba9ef70b1e23283ae820a2c3387b529f04ce09cf318597b33f5d59a52",
        },
    }

    for k, v in versions.items():
      http_file(
         name = k,
         executable = 1,
         sha256 = v["sha"], 
         urls = ["https://github.com/operator-framework/operator-sdk/releases/download/v1.37.0/{}".format(v["file"])],
      )

def install_kustomize():
    http_archive(
           name = "kustomize_darwin_arm",
           sha256 = "9556143d01feb9d9fa7706a6b0f60f74617c808f1c8c06130647e36a4e6a8746",
           urls = ["https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv4.4.0/kustomize_v4.4.0_darwin_arm64.tar.gz"],
           build_file_content = """
filegroup(
    name = "file",
    srcs = [
    "kustomize",
    ],
    visibility = ["//visibility:public"],
)
""",
    )

    http_archive(
       name = "kustomize_darwin",
       sha256 = "77898f8b7c37e3ba0c555b4b7c6d0e3301127fa0de7ade6a36ed767ca1715643",
       urls = ["https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv4.3.0/kustomize_v4.3.0_darwin_amd64.tar.gz"],
       build_file_content = """
filegroup(
    name = "file",
    srcs = [
	"kustomize",
    ],
    visibility = ["//visibility:public"],
)
""",
    )

    http_archive(
        name = "kustomize_linux",
        sha256 = "d34818d2b5d52c2688bce0e10f7965aea1a362611c4f1ddafd95c4d90cb63319",
        urls = ["https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv4.3.0/kustomize_v4.3.0_linux_amd64.tar.gz"],
        build_file_content = """
filegroup(
    name = "file",
    srcs = [
	"kustomize",
    ],
    visibility = ["//visibility:public"],
)
""",
    )

## Fetch opm used on generating csv
def install_opm():
    versions = OPENSHIFT_BINS["opm"]

    for k, v in versions.items():
      http_archive(
          name = k,
          sha256 = v["sha"],
          urls = [v["url"]],
          build_file_content = """
filegroup(
    name = "file",
    srcs = ["{src}"],
    visibility = ["//visibility:public"],
)
""".format(src=v["src"])
			)

## Fetch openshift-installer
def install_openshift():
    versions = OPENSHIFT_BINS["openshift-install"]

    for k, v in versions.items():
      http_archive(
          name = k,
          sha256 = v["sha"],
          urls = [v["url"]],
          build_file_content = """
filegroup(
    name = "file",
    srcs = ["openshift-install"],
    visibility = ["//visibility:public"],
)
"""
      )

def install_preflight():
    versions = OPENSHIFT_BINS["preflight"]

    for k, v in versions.items():
        http_file(
            name = k,
            executable = 1,
            sha256 = v["sha"],
            urls = [v["url"]]
        )

## Fetch crdb used in our container
def install_crdb():
    http_archive(
       name = "crdb_darwin", # todo fix or remove
       sha256 = "79fb1669678b802ae891ec3e005efa801c0f970fec4eb6af7ac89cdb6b991b42",
       urls = ["https://binaries.cockroachdb.com/cockroach-v22.2.19.darwin-10.9-amd64.tgz"],
       build_file_content = """
filegroup(
    name = "file",
    srcs = [
        "cockroach-v22.2.19.darwin-10.9-amd64/cockroach",
    ],
    visibility = ["//visibility:public"],
)
""",
   )

    http_archive(
           name = "crdb_linux_arm64",
           sha256 = "9fefea6e5c9715396648bb8865f55b10946b758de7997ccddb4876db56bcbb7a",
           urls = ["https://binaries.cockroachdb.com/cockroach-v22.2.19.linux-arm64.tgz"],
           build_file_content = """
filegroup(
     name = "file",
     srcs = [
            "cockroach-v22.2.19.linux-arm64/cockroach",
     ],
     visibility = ["//visibility:public"],
)
""",
)

    http_archive(
        name = "crdb_linux_amd64",
        sha256 = "3b48271a6fd62c2e5866b97ff9d5790d945a1d35ebf1815dc972d94327b4355b",
        urls = ["https://binaries.cockroachdb.com/cockroach-v22.2.19.linux-amd64.tgz"],
        build_file_content = """
filegroup(
    name = "file",
    srcs = [
        "cockroach-v22.2.19.linux-amd64/cockroach",
    ],
    visibility = ["//visibility:public"],
)
""",
   )

# fetch binaries for aws kubetest2
def install_kubetest2_aws():
    http_file(
        name = "aws-k8s-tester-linux",
        executable = 1,
        sha256 = "f6a95feef94ab9a96145fff812eeebae14f974edfead98046351f3098808df54",
        urls = ["https://github.com/aws/aws-k8s-tester/releases/download/v1.6.1/aws-k8s-tester-v1.6.1-linux-amd64"],
    )
    http_file(
            name = "aws-k8s-tester-m1",
            executable = 1,
            sha256 = "a0c4d6125c0dac4d5333560243975ecc2ef7712b71f5bd29e79c3f450ec7165e",
            urls = ["https://github.com/aws/aws-k8s-tester/releases/download/v1.6.5/aws-k8s-tester-v1.6.5-darwin-arm64"],
    )
    http_file(
        name = "aws-k8s-tester-darwin",
        executable = 1,
        sha256 = "a724288e8f2b87df89c711ed59fa2a09db5ad2b50a35cb3039fa610408b99b32",
        urls = ["https://github.com/aws/aws-k8s-tester/releases/download/v1.6.1/aws-k8s-tester-v1.6.1-darwin-amd64"],
    )
