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

package e2e

import (
	"fmt"
	"os/exec"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/agentic-layer/tool-gateway-agentgateway/test/utils"
)

// operatorName is the name of the operator being tested
const operatorName = "tool-gateway-agentgateway"

// namespace where the project is deployed in
const namespace = "tool-gateway-agentgateway-system"

var (
	// projectImage is the name of the image which will be build and loaded
	// with the code source changes to be tested.
	projectImage = "example.com/tool-gateway-agentgateway:v0.0.1"
)

// TestE2E runs the end-to-end (e2e) test suite for the project. These tests execute in an isolated,
// temporary environment to validate project changes with the purposed to be used in CI jobs.
// The default setup requires Kind, builds/loads the Manager Docker image locally, and installs
// CertManager.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting tool-gateway-agentgateway integration test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	By("building the manager(Operator) image")
	_, err := utils.Run(exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectImage)))
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager(Operator) image")

	By("loading the manager(Operator) image on Kind")
	err = utils.LoadImageToKindClusterWithName(projectImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager(Operator) image into Kind")

	// Deploy the operator
	By("creating manager namespace")
	_, err = utils.Run(exec.Command("kubectl", "create", "ns", namespace))
	Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

	By("labeling the namespace to enforce the restricted security policy")
	_, err = utils.Run(exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
		"pod-security.kubernetes.io/enforce=restricted"))
	Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

	By("installing CRDs")
	_, err = utils.Run(exec.Command("make", "install"))
	Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

	By("deploying the controller-manager")
	_, err = utils.Run(exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage)))
	Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
})

var _ = AfterSuite(func() {
	By("undeploying the controller-manager")
	_, _ = utils.Run(exec.Command("make", "undeploy"))

	By("uninstalling CRDs")
	_, _ = utils.Run(exec.Command("make", "uninstall"))

	By("removing manager namespace")
	_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", namespace))
})

func fetchKubernetesEvents() {
	By("Fetching Kubernetes events")
	eventsOutput, err := utils.Run(exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp"))
	if err == nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
	}
}

func fetchControllerManagerPodLogs() {
	By("Fetching controller manager pod logs")
	controllerLogs, err := utils.Run(exec.Command("kubectl", "logs",
		"-l", "app.kubernetes.io/name="+operatorName, "-n", namespace))
	if err == nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
	}
}
