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

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sigs.k8s.io/kubetest2/pkg/process"
)

var (
	clusterName string
	dir         string
	version     string
)

// main runs a simple smoke test that ensures that the operator brings up the database and that it's all functioning as
// expected.
//
// It roughly follows the steps we provide in our docs:
// https://www.cockroachlabs.com/docs/stable/deploy-cockroachdb-with-kubernetes.html#step-2-start-cockroachdb
//
// It will:
// * Start a cluster
// * Install the CRDs and the operator from the install dir
// * Apply the smoketest cluster
// * Add the client-secure-operator
// * Run the bank workload
func main() {
	flag.StringVar(&clusterName, "cluster", "smoketest", "the name of the cluster")
	flag.StringVar(&dir, "dir", ".", "the directory run in")
	flag.StringVar(&version, "version", "1.22.1", "the (patch) version of kubernetes")
	flag.Parse()

	// We prefer the caller's tools so CI uses its matching kubectl,
	// with the bundled tools available as fallbacks.
	path := os.Getenv("PATH")
	os.Setenv("PATH", path+string(os.PathListSeparator)+filepath.Join(os.Getenv("PWD"), "hack", "bin"))

	// change to the desired dir (typically $BUILD_WORKSPACE_DIRECTORY)
	if err := os.Chdir(dir); err != nil {
		bail(err)
	}

	steps := []Step{
		StartCluster(clusterName, version),
		ApplyManifest(filepath.Join("install", "crds.yaml")),
		ApplyManifest(filepath.Join("install", "operator.yaml")),
		WaitForDeploymentAvailable("cockroach-operator-manager", "cockroach-operator-system"),
		WaitForSecret("cockroach-operator-webhook-ca", "cockroach-operator-system"),
		sleep(10 * time.Second), // just give the manager time to write the TLS certs to disk
		ApplyManifest(filepath.Join("examples", "smoketest.yaml")),
		WaitForStatefulSetRollout("cockroachdb"),
		ApplyManifest(filepath.Join("examples", "client-secure-operator.yaml")),
		WaitForPodReady("cockroachdb-client-secure"),
		InitBankWorkload(),
		RunBankWorkload(10 * time.Second),
	}

	defer func(cluster Step, fn ExecFn) {
		if err := cluster.Apply(fn); err != nil {
			fmt.Print(err.Error())

		}
	}(StopCluster(clusterName), process.ExecJUnit)

	for _, step := range steps {
		if err := step.Apply(process.ExecJUnit); err != nil {
			bail(err)
		}
	}
}

func bail(err error) {
	fmt.Fprintf(os.Stderr, "OOPS! An error occurred: %s\n", err)
	if err = StopCluster(clusterName).Apply(process.ExecJUnit); err != nil {
		fmt.Print(err.Error())
	}
	os.Exit(1)
}

func sleep(d time.Duration) Step {
	return StepFn(func(_ ExecFn) error {
		time.Sleep(d)
		return nil
	})
}
