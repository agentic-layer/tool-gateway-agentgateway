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
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/agentic-layer/tool-gateway-kgateway/test/utils"
)

const (
	testNamespace       = "showcase-news"
	testToolGatewayName = "test-tool-gateway"
	testToolServerName  = "website-fetcher"
)

var _ = Describe("ToolGateway", Ordered, func() {
	var (
		toolGatewayYAML string
		toolServerYAML  string
	)

	BeforeAll(func() {
		// Create test namespace
		By("creating test namespace")
		_, err := utils.Run(exec.Command("kubectl", "create", "ns", testNamespace))
		Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")

		// Create ToolGateway YAML
		toolGatewayYAML = filepath.Join("/tmp", "test-toolgateway.yaml")
		toolGatewayContent := `apiVersion: runtime.agentic-layer.ai/v1alpha1
kind: ToolGateway
metadata:
  name: test-tool-gateway
  namespace: showcase-news
spec:
  toolGatewayClassName: kgateway
`
		err = os.WriteFile(toolGatewayYAML, []byte(toolGatewayContent), 0o644)
		Expect(err).NotTo(HaveOccurred(), "Failed to write ToolGateway YAML")

		// Create ToolServer YAML
		toolServerYAML = filepath.Join("/tmp", "test-toolserver.yaml")
		toolServerContent := `apiVersion: runtime.agentic-layer.ai/v1alpha1
kind: ToolServer
metadata:
  name: website-fetcher
  namespace: showcase-news
spec:
  protocol: mcp
  transportType: http
  image: nginx:latest
  port: 8000
  path: /mcp
  replicas: 1
`
		err = os.WriteFile(toolServerYAML, []byte(toolServerContent), 0o644)
		Expect(err).NotTo(HaveOccurred(), "Failed to write ToolServer YAML")
	})

	AfterAll(func() {
		// Clean up test resources
		By("deleting ToolGateway")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "-f", toolGatewayYAML))

		By("deleting ToolServer")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "-f", toolServerYAML))

		By("deleting test namespace")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", testNamespace))

		// Clean up temporary files
		_ = os.Remove(toolGatewayYAML)
		_ = os.Remove(toolServerYAML)
	})

	AfterEach(func() {
		// After each test, check for failures and collect logs, events for debugging.
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			fetchKubernetesEvents()
			fetchControllerManagerPodLogs()

			By("Fetching Gateway status")
			gatewayOutput, err := utils.Run(exec.Command("kubectl", "get", "gateway",
				"agentgateway-proxy", "-n", "agentgateway-system", "-o", "yaml"))
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Gateway status:\n%s", gatewayOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Gateway status: %s", err)
			}

			By("Fetching HTTPRoute status")
			routeOutput, err := utils.Run(exec.Command("kubectl", "get", "httproute",
				testToolServerName, "-n", testNamespace, "-o", "yaml"))
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "HTTPRoute status:\n%s", routeOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get HTTPRoute status: %s", err)
			}

			By("Fetching AgentgatewayBackend status")
			backendOutput, err := utils.Run(exec.Command("kubectl", "get", "agentgatewaybackend",
				testToolServerName, "-n", testNamespace, "-o", "yaml"))
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "AgentgatewayBackend status:\n%s", backendOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get AgentgatewayBackend status: %s", err)
			}
		}
	})

	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(5 * time.Second)

	It("should create Gateway when ToolGateway is created", func() {
		By("creating ToolGateway resource")
		_, err := utils.Run(exec.Command("kubectl", "apply", "-f", toolGatewayYAML))
		Expect(err).NotTo(HaveOccurred(), "Failed to create ToolGateway")

		By("waiting for agentgateway-proxy Gateway to be created")
		Eventually(func(g Gomega) {
			output, err := utils.Run(exec.Command("kubectl", "get", "gateway",
				"agentgateway-proxy", "-n", "agentgateway-system", "-o", "jsonpath={.metadata.name}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("agentgateway-proxy"))
		}).Should(Succeed())

		By("verifying Gateway has correct gatewayClassName")
		Eventually(func(g Gomega) {
			output, err := utils.Run(exec.Command("kubectl", "get", "gateway",
				"agentgateway-proxy", "-n", "agentgateway-system",
				"-o", "jsonpath={.spec.gatewayClassName}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("agentgateway"))
		}).Should(Succeed())

		By("verifying Gateway has HTTP listener on port 80")
		Eventually(func(g Gomega) {
			output, err := utils.Run(exec.Command("kubectl", "get", "gateway",
				"agentgateway-proxy", "-n", "agentgateway-system",
				"-o", "jsonpath={.spec.listeners[0].port}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("80"))
		}).Should(Succeed())
	})

	It("should create AgentgatewayBackend and HTTPRoute when ToolServer is created", func() {
		By("creating ToolServer resource")
		_, err := utils.Run(exec.Command("kubectl", "apply", "-f", toolServerYAML))
		Expect(err).NotTo(HaveOccurred(), "Failed to create ToolServer")

		By("waiting for AgentgatewayBackend to be created")
		Eventually(func(g Gomega) {
			output, err := utils.Run(exec.Command("kubectl", "get", "agentgatewaybackend",
				testToolServerName, "-n", testNamespace, "-o", "jsonpath={.metadata.name}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal(testToolServerName))
		}).Should(Succeed())

		By("verifying AgentgatewayBackend has correct spec")
		Eventually(func(g Gomega) {
			output, err := utils.Run(exec.Command("kubectl", "get", "agentgatewaybackend",
				testToolServerName, "-n", testNamespace, "-o", "jsonpath={.spec.mcp.targets[0].name}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("mcp-target"))
		}).Should(Succeed())

		By("waiting for HTTPRoute to be created")
		Eventually(func(g Gomega) {
			output, err := utils.Run(exec.Command("kubectl", "get", "httproute",
				testToolServerName, "-n", testNamespace, "-o", "jsonpath={.metadata.name}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal(testToolServerName))
		}).Should(Succeed())

		By("verifying HTTPRoute has correct parent reference")
		Eventually(func(g Gomega) {
			output, err := utils.Run(exec.Command("kubectl", "get", "httproute",
				testToolServerName, "-n", testNamespace,
				"-o", "jsonpath={.spec.parentRefs[0].name}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("agentgateway-proxy"))
		}).Should(Succeed())

		By("verifying HTTPRoute has correct backend reference")
		Eventually(func(g Gomega) {
			output, err := utils.Run(exec.Command("kubectl", "get", "httproute",
				testToolServerName, "-n", testNamespace,
				"-o", "jsonpath={.spec.rules[0].backendRefs[0].name}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal(testToolServerName))
		}).Should(Succeed())

		By("verifying HTTPRoute has correct path match")
		Eventually(func(g Gomega) {
			output, err := utils.Run(exec.Command("kubectl", "get", "httproute",
				testToolServerName, "-n", testNamespace,
				"-o", "jsonpath={.spec.rules[0].matches[0].path.value}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("/mcp"))
		}).Should(Succeed())
	})
})
