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
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/agentic-layer/tool-gateway-kgateway/test/utils"
)

var _ = Describe("ToolGateway E2E", Ordered, func() {
	const (
		namespace       = "test-namespace"
		toolGatewayName = "test-tool-gateway"
		toolServerName  = "echo-mcp-server"
		gatewayPort     = 80
		testTimeout     = 5 * time.Minute
		pollInterval    = 5 * time.Second
	)

	var projectDir string

	BeforeAll(func() {
		By("Getting the project directory")
		var err error
		projectDir, err = utils.GetProjectDir()
		Expect(err).NotTo(HaveOccurred())
		Expect(projectDir).NotTo(BeEmpty())

		By("Applying the samples.yaml file")
		samplesPath := filepath.Join(projectDir, "config", "samples", "samples.yaml")
		cmd := exec.Command("kubectl", "apply", "-f", samplesPath)
		output, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to apply samples: %s", string(output)))

		By("Waiting for namespace to be ready")
		Eventually(func() error {
			cmd := exec.Command("kubectl", "get", "namespace", namespace)
			return cmd.Run()
		}, testTimeout, pollInterval).Should(Succeed())
	})

	AfterAll(func() {
		By("Cleaning up the samples")
		samplesPath := filepath.Join(projectDir, "config", "samples", "samples.yaml")
		cmd := exec.Command("kubectl", "delete", "-f", samplesPath, "--ignore-not-found=true")
		_ = cmd.Run() // Best effort cleanup
	})

	Context("When samples are applied", func() {
		It("should create the ToolGateway resource", func() {
			By("Verifying ToolGateway exists")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "toolgateway", toolGatewayName, "-n", namespace)
				return cmd.Run()
			}, testTimeout, pollInterval).Should(Succeed())
		})

		It("should create the Gateway resource", func() {
			By("Verifying Gateway exists")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "gateway", toolGatewayName, "-n", namespace)
				return cmd.Run()
			}, testTimeout, pollInterval).Should(Succeed())

			By("Verifying Gateway is configured correctly")
			cmd := exec.Command("kubectl", "get", "gateway", toolGatewayName, "-n", namespace, "-o", "json")
			output, err := cmd.Output()
			Expect(err).NotTo(HaveOccurred())

			var gateway map[string]interface{}
			err = json.Unmarshal(output, &gateway)
			Expect(err).NotTo(HaveOccurred())

			spec := gateway["spec"].(map[string]interface{})
			Expect(spec["gatewayClassName"]).To(Equal("agentgateway"))
		})

		It("should create the ToolServer resource", func() {
			By("Verifying ToolServer exists")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "toolserver", toolServerName, "-n", namespace)
				return cmd.Run()
			}, testTimeout, pollInterval).Should(Succeed())
		})

		It("should create the AgentgatewayBackend resource", func() {
			By("Verifying AgentgatewayBackend exists")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "agentgatewaybackend", toolServerName, "-n", namespace)
				return cmd.Run()
			}, testTimeout, pollInterval).Should(Succeed())
		})

		It("should create the HTTPRoute resource", func() {
			By("Verifying HTTPRoute exists")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "httproute", toolServerName, "-n", namespace)
				return cmd.Run()
			}, testTimeout, pollInterval).Should(Succeed())
		})

		It("should allow MCP initialization request through the Gateway", func() {
			By("Waiting for ToolServer pod to be ready")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "wait", "--for=condition=ready", "pod",
					"-l", fmt.Sprintf("app=%s", toolServerName),
					"-n", namespace,
					"--timeout=300s")
				return cmd.Run()
			}, testTimeout, pollInterval).Should(Succeed())

			By("Sending MCP initialize request through Gateway service")
			mcpRequest := map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "initialize",
				"params": map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]interface{}{},
					"clientInfo": map[string]interface{}{
						"name":    "test-client",
						"version": "1.0.0",
					},
				},
				"id": 1,
			}

			var responseBody []byte
			var statusCode int
			var err error

			Eventually(func() error {
				responseBody, statusCode, err = utils.MakeServicePost(
					namespace,
					toolGatewayName,
					gatewayPort,
					"/mcp",
					mcpRequest,
				)
				if err != nil {
					return err
				}
				if statusCode != http.StatusOK {
					return fmt.Errorf("unexpected status code: %d", statusCode)
				}
				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("Verifying MCP response")
			var mcpResponse map[string]interface{}
			err = json.Unmarshal(responseBody, &mcpResponse)
			Expect(err).NotTo(HaveOccurred())

			// Verify it's a valid MCP response
			Expect(mcpResponse).To(HaveKey("jsonrpc"))
			Expect(mcpResponse["jsonrpc"]).To(Equal("2.0"))
			Expect(mcpResponse).To(HaveKey("id"))
			Expect(mcpResponse["id"]).To(BeEquivalentTo(1))
		})
	})
})
