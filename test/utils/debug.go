/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
)

// systemNamespacePrefixes are skipped when auto-collecting diagnostics.
var systemNamespacePrefixes = []string{"kube-", "cert-manager", "local-path-storage", "gmp-"}

// systemNamespaces are skipped when auto-collecting diagnostics (exact match).
var systemNamespaces = map[string]bool{
	"tool-gateway-agentgateway-system": true, // controller logs are captured separately
}

// interestingKinds are dumped cluster-wide to capture operator-managed state.
var interestingKinds = []string{
	"toolgateway",
	"toolserver",
	"toolgatewayclass",
	"guard",
	"guardrailprovider",
	"gateway.gateway.networking.k8s.io",
	"httproute.gateway.networking.k8s.io",
	"agentgatewaypolicy.agentgateway.dev",
	"agentgatewaybackend.agentgateway.dev",
	"agentgatewayparameters.agentgateway.dev",
}

// newDebugDir creates a fresh timestamped directory under $TMPDIR to collect
// failure artifacts for a single test. Returns the absolute path.
func newDebugDir(prefix string) (string, error) {
	ts := time.Now().UTC().Format("20060102T150405Z")
	safe := strings.ReplaceAll(prefix, "/", "_")
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("%s-%s", safe, ts))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create debug dir %s: %w", dir, err)
	}
	return dir, nil
}

// runKubectl runs `kubectl args...` and writes combined output to <dir>/<filename>.
func runKubectl(dir, filename string, args ...string) {
	path := filepath.Join(dir, filename)
	out, err := exec.Command("kubectl", args...).CombinedOutput()
	if err != nil {
		out = append(out, []byte(fmt.Sprintf("\n\n[kubectl error: %v]\n", err))...)
	}
	_ = os.WriteFile(path, out, 0o644)
}

// listUserNamespaces returns all cluster namespaces minus the ones matching
// systemNamespacePrefixes or systemNamespaces.
func listUserNamespaces() []string {
	out, err := exec.Command("kubectl", "get", "ns", "-o", "name").CombinedOutput()
	if err != nil {
		_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "failed to list namespaces: %v\n%s\n", err, out)
		return nil
	}
	var result []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		ns := strings.TrimPrefix(line, "namespace/")
		if ns == "" || systemNamespaces[ns] {
			continue
		}
		skip := false
		for _, p := range systemNamespacePrefixes {
			if strings.HasPrefix(ns, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		result = append(result, ns)
	}
	return result
}

// listPods returns pod names in the given namespace (without the "pod/" prefix).
func listPods(namespace string) []string {
	out, err := exec.Command("kubectl", "get", "pods", "-n", namespace, "-o", "name").CombinedOutput()
	if err != nil {
		return nil
	}
	var pods []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.TrimPrefix(line, "pod/")
		if name != "" {
			pods = append(pods, name)
		}
	}
	return pods
}

// CollectDiagnostics writes pod logs, events, and a cluster-wide dump of
// interesting custom resources to a timestamped directory under $TMPDIR.
// It enumerates user namespaces automatically (skipping kube-* and cert-manager).
// The directory path is printed to GinkgoWriter and returned.
//
// Intended for use from AfterEach on test failure.
func CollectDiagnostics(prefix string) string {
	dir, err := newDebugDir(prefix)
	if err != nil {
		_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "failed to create debug dir: %v\n", err)
		return ""
	}
	_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "Debug bundle: %s\n", dir)

	// Cluster-wide overviews.
	runKubectl(dir, "pods_all.txt", "get", "pods", "-A", "-o", "wide")
	runKubectl(dir, "namespaces.txt", "get", "ns")

	// Per-namespace logs and events for user namespaces.
	for _, ns := range listUserNamespaces() {
		runKubectl(dir, fmt.Sprintf("events_%s.txt", ns),
			"get", "events", "-n", ns, "--sort-by=.lastTimestamp")

		for _, pod := range listPods(ns) {
			runKubectl(dir, fmt.Sprintf("logs_%s_%s.log", ns, pod),
				"logs", "-n", ns, pod, "--all-containers=true", "--prefix=true", "--tail=500")
		}
	}

	// Cluster-wide dump of interesting kinds (best effort; some may not exist).
	for _, kind := range interestingKinds {
		safe := strings.ReplaceAll(kind, ".", "_")
		runKubectl(dir, fmt.Sprintf("%s.yaml", safe),
			"get", kind, "-A", "-o", "yaml")
	}

	return dir
}
