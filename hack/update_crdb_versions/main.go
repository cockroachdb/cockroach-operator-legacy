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
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	semver "github.com/Masterminds/semver/v3"
	"github.com/cockroachdb/errors"
	"gopkg.in/yaml.v2"
)

const (
	cockroachDBRegistry    = "cockroachdb"
	cockroachDBImage       = "cockroach"
	httpTimeoutSecs        = 30
	dockerRegistryPageSize = 1000
	maxRequestAttempts     = 6
	maxRetryWait           = 60 * time.Second
	repo                   = "registry.connect.redhat.com/cockroachdb/cockroach"
	versionsFile           = "crdb-versions.yaml"

	fileHeader = `#
# Supported CockroachDB versions.
#
# This file contains a list of CockroachDB versions that are supported by the
# operator. hack/crdbversions/main.go uses this list to generate various
# manifests.
# Please update this file when CockroachDB releases new versions.
#
# Generated. DO NOT EDIT. This file is created via make release/gen-templates

`

	// TODO(rail): we may need to add pagination handling in case we pass 500 versions
	// Use anonymous API to get the list of published images from the RedHat Catalog.
	reqPath = "/api/containers/v1/repositories/registry/registry.connect.redhat.com/" +
		"repository/cockroachdb/cockroach/images?" +
		"include=data.docker_image_digest,data.repositories&page_size=500&page=0"
)

var (
	// invalidVersions are known bad versions or non-semver versions that should not
	// be included in the results.
	invalidVersions = regexp.MustCompile("^v19|^v21.1.8$|latest|ubi$")

	// semVerRegex defines a Regexp for ensuring a valid (non-prerelease) version.
	semVerRegex = regexp.MustCompile(`v?([0-9]+)(\.[0-9]+)?(\.[0-9]+)?$`)

	DockerRegistryTagsURL = fmt.Sprintf("https://registry-1.docker.io/v2/%s/%s/tags/list",
		cockroachDBRegistry, cockroachDBImage)
	DockerRegistryTokenURL = fmt.Sprintf("https://auth.docker.io/token?service=registry.docker.io&scope=repository:%s/%s:pull",
		cockroachDBRegistry, cockroachDBImage)
)

func main() {
	allowRemovals := flag.Bool("allow-removals", false,
		"allow versions in the existing crdb-versions.yaml to be removed")
	flag.Parse()

	path := filepath.Join(os.Getenv("BUILD_WORKSPACE_DIRECTORY"), versionsFile)
	if err := UpdateVersionsFile(path, "https://catalog.redhat.com/", *allowRemovals); err != nil {
		panic(err)
	}
}

// UpdateVersionsFile reads the existing versions file, generates the updated set
// of supported versions, enforces the removal guard (unless allowRemovals is
// set), and writes the result back to path. It is the single entry point that
// wires reading, generation, validation, and writing together so that flow can
// be exercised end to end in tests.
func UpdateVersionsFile(path, baseURL string, allowRemovals bool) error {
	existing, err := readExistingVersions(path)
	if err != nil {
		return err
	}

	updated, err := GenerateCrdbVersions(baseURL)
	if err != nil {
		return err
	}

	if !allowRemovals {
		if err := ValidateNoVersionRemovals(tagsOf(existing), tagsOf(updated)); err != nil {
			return err
		}
	}

	data, err := yaml.Marshal(updated)
	if err != nil {
		return errors.Wrap(err, "marshaling YAML")
	}

	out := append([]byte(fileHeader), data...)
	if err := os.WriteFile(path, out, 0644); err != nil {
		return errors.Wrap(err, "writing versions file")
	}

	return nil
}

// readExistingVersions loads the current versions file. A missing file is
// treated as an empty baseline so the tool can bootstrap the file on first run.
func readExistingVersions(path string) (*yamlOutput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &yamlOutput{}, nil
		}
		return nil, errors.Wrap(err, "reading existing versions file")
	}

	var existing yamlOutput
	if err := yaml.Unmarshal(data, &existing); err != nil {
		return nil, errors.Wrap(err, "parsing existing versions file")
	}

	return &existing, nil
}

// GenerateCrdbVersions fetches all published versions of CRDB from the RedHat
// connect registry, keeping only tags that are also published to the
// cockroachdb/cockroach Docker Hub repository, and returns them sorted by
// version.
func GenerateCrdbVersions(baseURL string) (*yamlOutput, error) {
	client := &http.Client{Timeout: httpTimeoutSecs * time.Second}
	resp, err := fetchAPIResponse(client, fmt.Sprintf("%s%s", baseURL, reqPath))
	if err != nil {
		return nil, err
	}

	dockerHubTags, err := fetchDockerRegistryTags(client)
	if err != nil {
		return nil, err
	}

	return generateOutput(resp, dockerHubTags), nil
}

func fetchAPIResponse(client *http.Client, url string) (*apiResponse, error) {
	r, err := getWithRetry(client, url, nil)
	if err != nil {
		return nil, errors.Wrap(err, "fetching CRDB versions")
	}
	defer r.Body.Close()

	var resp apiResponse
	if err := json.NewDecoder(r.Body).Decode(&resp); err != nil {
		return nil, errors.Wrap(err, "decoding API response")
	}

	return &resp, nil
}

// Use the Registry API rather than the Hub API, which caps anonymous pagination.
// Public pull access needs only a short-lived anonymous token, so this also works
// in pull-request workflows without Docker account credentials.
func fetchDockerRegistryTags(client *http.Client) (map[string]bool, error) {
	token, err := fetchDockerRegistryToken(client)
	if err != nil {
		return nil, err
	}
	headers := http.Header{"Authorization": {"Bearer " + token}}
	nextURL, err := url.Parse(DockerRegistryTagsURL)
	if err != nil {
		return nil, errors.Wrap(err, "parsing Docker registry URL")
	}
	query := nextURL.Query()
	query.Set("n", strconv.Itoa(dockerRegistryPageSize))
	nextURL.RawQuery = query.Encode()

	tags := make(map[string]bool)
	seenPages := make(map[string]bool)
	for nextURL != nil {
		if seenPages[nextURL.String()] {
			return nil, errors.Errorf("Docker registry pagination loop at %s", nextURL)
		}
		seenPages[nextURL.String()] = true

		r, err := getWithRetry(client, nextURL.String(), headers)
		if err != nil {
			return nil, errors.Wrap(err, "fetching Docker registry tags")
		}

		var page struct {
			Tags []string `json:"tags"`
		}
		decodeErr := json.NewDecoder(r.Body).Decode(&page)
		closeErr := r.Body.Close()
		if decodeErr != nil {
			return nil, errors.Wrap(decodeErr, "decoding Docker registry tags")
		}
		if closeErr != nil {
			return nil, errors.Wrap(closeErr, "closing Docker registry response")
		}

		for _, tag := range page.Tags {
			tags[tag] = true
		}

		nextURL, err = nextRegistryPage(nextURL, r.Header.Values("Link"))
		if err != nil {
			return nil, err
		}
	}

	return tags, nil
}

func fetchDockerRegistryToken(client *http.Client) (string, error) {
	r, err := getWithRetry(client, DockerRegistryTokenURL, nil)
	if err != nil {
		return "", errors.Wrap(err, "fetching Docker registry token")
	}
	defer r.Body.Close()
	var response struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&response); err != nil {
		return "", errors.Wrap(err, "decoding Docker registry token")
	}
	if response.Token == "" {
		return "", errors.New("Docker registry returned an empty token")
	}
	return response.Token, nil
}

func nextRegistryPage(current *url.URL, links []string) (*url.URL, error) {
	for _, header := range links {
		for _, link := range strings.Split(header, ",") {
			target, parameters, ok := strings.Cut(strings.TrimSpace(link), ">")
			if !ok || !strings.HasPrefix(target, "<") {
				return nil, errors.New("invalid Docker registry pagination link")
			}
			_, attributes, err := mime.ParseMediaType("link" + parameters)
			if err != nil {
				return nil, errors.Wrap(err, "parsing Docker registry pagination link")
			}
			if attributes["rel"] != "next" {
				continue
			}
			next, err := url.Parse(strings.TrimPrefix(target, "<"))
			if err != nil {
				return nil, errors.Wrap(err, "parsing Docker registry page URL")
			}
			next = current.ResolveReference(next)
			// Keep the bearer token on the repository endpoint that issued the link.
			if next.Scheme != current.Scheme || next.Host != current.Host || next.Path != current.Path {
				return nil, errors.New("Docker registry pagination link changed repository endpoint")
			}
			return next, nil
		}
	}
	return nil, nil
}

func getWithRetry(client *http.Client, url string, headers http.Header) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= maxRequestAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header = headers.Clone()
		r, err := client.Do(req)
		if err == nil && r.StatusCode >= http.StatusOK && r.StatusCode < http.StatusMultipleChoices {
			return r, nil
		}

		var delay time.Duration
		if err != nil {
			lastErr = err
			delay = retryBackoff(attempt)
		} else {
			lastErr = errors.Errorf("GET %s returned %s", url, r.Status)
			shouldRetry := r.StatusCode == http.StatusTooManyRequests || r.StatusCode >= http.StatusInternalServerError
			delay = retryDelay(r, attempt)
			r.Body.Close()
			if !shouldRetry {
				return nil, lastErr
			}
		}

		if attempt < maxRequestAttempts {
			fmt.Printf("request failed (attempt %d/%d): %v; retrying in %s\n",
				attempt, maxRequestAttempts, lastErr, delay)
			time.Sleep(delay)
		}
	}

	return nil, errors.Wrapf(lastErr, "request failed after %d attempts", maxRequestAttempts)
}

// retryDelay decides how long to wait before the next attempt, preferring the
// server's own guidance (Retry-After, then Docker Hub's X-RateLimit-Reset) and
// falling back to jittered exponential backoff. All waits are capped so a
// long-lived rate limit fails the run rather than hanging CI.
func retryDelay(r *http.Response, attempt int) time.Duration {
	if retryAfter := r.Header.Get("Retry-After"); retryAfter != "" {
		if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
			return capBackoff(time.Duration(seconds) * time.Second)
		}
		if when, err := http.ParseTime(retryAfter); err == nil {
			if delay := time.Until(when); delay > 0 {
				return capBackoff(delay)
			}
		}
	}

	// Docker Hub advertises when the limit resets via X-RateLimit-Reset, a Unix
	// timestamp, rather than Retry-After.
	if reset := r.Header.Get("X-RateLimit-Reset"); reset != "" {
		if ts, err := strconv.ParseInt(reset, 10, 64); err == nil {
			if delay := time.Until(time.Unix(ts, 0)); delay > 0 {
				return capBackoff(delay)
			}
		}
	}

	return retryBackoff(attempt)
}

// retryBackoff returns exponential backoff for the given attempt with up to 50%
// added jitter, capped at maxRetryWait. Jitter keeps concurrent jobs sharing an
// egress IP from retrying against a rate-limited endpoint in lockstep.
func retryBackoff(attempt int) time.Duration {
	backoff := capBackoff(time.Duration(1<<(attempt-1)) * time.Second)
	return backoff + time.Duration(rand.Int63n(int64(backoff/2)+1))
}

func capBackoff(d time.Duration) time.Duration {
	if d > maxRetryWait {
		return maxRetryWait
	}
	return d
}

func generateOutput(resp *apiResponse, dockerHubTags map[string]bool) *yamlOutput {
	output := new(yamlOutput)
	usedTags := make(map[string]bool)
	for _, data := range resp.Data {
		for _, r := range data.Repos {
			for _, tag := range r.Tags {
				if !isValid(tag.Name) || usedTags[tag.Name] || !dockerHubTags[tag.Name] {
					continue
				}
				usedTags[tag.Name] = true
				output.CrdbVersions = append(output.CrdbVersions, version{
					Image:       fmt.Sprintf("%s/%s:%s", cockroachDBRegistry, cockroachDBImage, tag.Name),
					RedhatImage: fmt.Sprintf("%s@%s", repo, data.Digest),
					Tag:         tag.Name,
				})
			}
		}
	}

	// ensure results are sorted properly by version
	sort.Slice(output.CrdbVersions, func(i, j int) bool {
		// safe to ignore error due to previous regexp check in isValid
		v1, _ := semver.NewVersion(output.CrdbVersions[i].Tag)
		v2, _ := semver.NewVersion(output.CrdbVersions[j].Tag)
		return v1.LessThan(v2)
	})

	return output
}

func isValid(tag string) bool {
	if invalidVersions.MatchString(tag) {
		return false
	}

	return semVerRegex.MatchString(tag)
}

// tagsOf extracts the version tags from a set of generated versions.
func tagsOf(o *yamlOutput) []string {
	tags := make([]string, 0, len(o.CrdbVersions))
	for _, v := range o.CrdbVersions {
		tags = append(tags, v.Tag)
	}
	return tags
}

// ValidateNoVersionRemovals returns an error if a previously supported version
// is absent from the updated version list.
func ValidateNoVersionRemovals(existing, updated []string) error {
	updatedTags := make(map[string]bool, len(updated))
	for _, tag := range updated {
		updatedTags[tag] = true
	}

	var removed []string
	for _, tag := range existing {
		if !updatedTags[tag] {
			removed = append(removed, tag)
		}
	}
	if len(removed) == 0 {
		return nil
	}

	sort.Strings(removed)
	return errors.Errorf("refusing to remove existing CRDB versions: %s; rerun with CRDB_VERSION_UPDATE_ARGS=-allow-removals for an intentional removal",
		strings.Join(removed, ", "))
}

// apiResponse encapsulates the response from the RH Catalog API.
type apiResponse struct {
	Data []struct {
		// Digest is used for digest pinning in OLM bundles (e.g. sha256@<sha>).
		Digest string `json:"docker_image_digest"`
		Repos  []struct {
			Tags []struct {
				// The image tag including the `v` prefix
				Name string `json:"name"`
			}
		} `json:"repositories"`
	} `json:"data"`
}

type yamlOutput struct {
	CrdbVersions []version `yaml:"CrdbVersions"`
}

type version struct {
	Image       string `yaml:"image"`
	RedhatImage string `yaml:"redhatImage"`
	Tag         string `yaml:"tag"`
}
