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

	"github.com/cockroachdb/cockroach-operator/pkg/resource"
	"github.com/go-logr/logr"
)

var NewDeploy = newDeploy

var NewVersionChecker = newVersionChecker

// CompleteVersionChecker exposes the shared tail of the version checker so
// tests can assert that status and annotations are written together.
func CompleteVersionChecker(
	ctx context.Context, a Actor, cluster *resource.Cluster, version, imageName string, log logr.Logger,
) error {
	return a.(*versionChecker).completeVersionChecker(ctx, cluster, version, imageName, log)
}
