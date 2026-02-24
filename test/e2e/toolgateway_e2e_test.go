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
	"os/exec"
	"sort"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/agentic-layer/tool-gateway-agentgateway/test/utils"
)

var _ = Describe("ToolGateway", Ordered, func() {

	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Collecting controller logs for debugging")
			cmd := exec.Command("kubectl", "logs", "-n", "tool-gateway-agentgateway-system",
				"-l", "control-plane=controller-manager", "--tail=100")
			output, _ := cmd.CombinedOutput()
			GinkgoWriter.Printf("Controller logs:\n%s\n", string(output))
		}
	})

	BeforeAll(func() {
		By("applying ToolGateway with ToolServer sample")
		_, err := utils.Run(exec.Command("kubectl", "apply",
			"-f", "config/samples/toolgateway_v1alpha1_toolgateway_with_toolserver.yaml"))
		Expect(err).NotTo(HaveOccurred(), "Failed to apply samples")
	})

	AfterAll(func() {
		By("cleaning up test resources")
		_, _ = utils.Run(exec.Command("kubectl", "delete",
			"-f", "config/samples/toolgateway_v1alpha1_toolgateway_with_toolserver.yaml"))
	})

	// mcpInitializeParams are the standard parameters for an MCP initialize request.
	mcpInitializeParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "test-client",
			"version": "1.0.0",
		},
	}

	// fetchTools performs a single attempt at the correct MCP protocol sequence within
	// one port-forward connection:
	//   1. initialize  → obtain Mcp-Session-Id
	//   2. tools/list  → pass Mcp-Session-Id header
	// Failures are reported via g so callers can embed this inside Eventually.
	fetchTools := func(g Gomega, path string) []string {
		body, statusCode, err := utils.MakeServiceRequest(
			"tool-gateway", "test-tool-gateway", 80,
			func(baseURL string) ([]byte, int, error) {
				// Step 1: initialize (required first per MCP spec)
				initReq := map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      1,
					"method":  "initialize",
					"params":  mcpInitializeParams,
				}
				_, initHeaders, initStatus, initErr := utils.PostRequestWithExtraHeaders(
					baseURL+path, initReq, nil)
				if initErr != nil {
					return nil, initStatus, fmt.Errorf("initialize request failed: %w", initErr)
				}
				if initStatus != 200 {
					return nil, initStatus, fmt.Errorf("initialize returned status %d", initStatus)
				}

				// Step 2: tools/list, forwarding the session ID if the server issued one
				listReq := map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      2,
					"method":  "tools/list",
				}
				extraHeaders := map[string]string{}
				if sessionID := initHeaders.Get("Mcp-Session-Id"); sessionID != "" {
					extraHeaders["Mcp-Session-Id"] = sessionID
				}
				listBody, _, listStatus, listErr := utils.PostRequestWithExtraHeaders(
					baseURL+path, listReq, extraHeaders)
				return listBody, listStatus, listErr
			},
		)
		_, _ = fmt.Fprintf(GinkgoWriter, "tools/list at %s: statusCode=%d err=%v\n", path, statusCode, err)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(statusCode).To(Equal(200))

		var responseMap map[string]interface{}
		g.Expect(json.Unmarshal(utils.ParseSSEBody(body), &responseMap)).To(Succeed())
		g.Expect(responseMap["jsonrpc"]).To(Equal("2.0"))
		g.Expect(responseMap).To(HaveKey("result"))

		result, ok := responseMap["result"].(map[string]interface{})
		g.Expect(ok).To(BeTrue(), "result should be an object")
		g.Expect(result).To(HaveKey("tools"))

		tools, ok := result["tools"].([]interface{})
		g.Expect(ok).To(BeTrue(), "tools should be an array")

		toolNames := make([]string, 0, len(tools))
		for _, tool := range tools {
			toolMap, ok := tool.(map[string]interface{})
			g.Expect(ok).To(BeTrue())
			toolNames = append(toolNames, toolMap["name"].(string))
		}
		sort.Strings(toolNames)
		return toolNames
	}

	It("should accept MCP initialize requests", func() {
		By("sending MCP initialize request to server-a")
		var resp map[string]interface{}
		Eventually(func(g Gomega) {
			initReq := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "initialize",
				"params":  mcpInitializeParams,
			}
			body, statusCode, err := utils.MakeServiceRequest(
				"tool-gateway", "test-tool-gateway", 80,
				func(baseURL string) ([]byte, int, error) {
					b, _, sc, e := utils.PostRequestWithExtraHeaders(baseURL+"/namespace-a/server-a/mcp", initReq, nil)
					return b, sc, e
				},
			)
			_, _ = fmt.Fprintf(GinkgoWriter, "initialize: statusCode=%d err=%v\n", statusCode, err)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(statusCode).To(Equal(200))
			g.Expect(json.Unmarshal(utils.ParseSSEBody(body), &resp)).To(Succeed())
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Failed to send MCP initialize to gateway")

		By("verifying MCP initialize response")
		Expect(resp["jsonrpc"]).To(Equal("2.0"))
		Expect(resp["id"]).To(BeEquivalentTo(1))
		Expect(resp).To(HaveKey("result"))
	})

	It("should expose individual server tools via /<namespace>/<server>/mcp", func() {
		By("listing tools from server-a")
		Eventually(func(g Gomega) {
			tools := fetchTools(g, "/namespace-a/server-a/mcp")
			g.Expect(tools).To(ContainElements("echo", "get_weather"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "tools from server-a did not match")

		By("listing tools from server-b")
		Eventually(func(g Gomega) {
			tools := fetchTools(g, "/namespace-a/server-b/mcp")
			g.Expect(tools).To(ContainElements("echo", "get_status"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "tools from server-b did not match")

		By("listing tools from server-c")
		Eventually(func(g Gomega) {
			tools := fetchTools(g, "/namespace-b/server-c/mcp")
			g.Expect(tools).To(ContainElements("echo", "get_info"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "tools from server-c did not match")
	})

	It("should aggregate all servers in a namespace via /<namespace>/mcp", func() {
		By("listing tools from namespace-a aggregate endpoint")
		Eventually(func(g Gomega) {
			tools := fetchTools(g, "/namespace-a/mcp")
			g.Expect(tools).To(ContainElements(
				"namespace-a-server-a_echo",
				"namespace-a-server-a_get_weather",
				"namespace-a-server-b_echo",
				"namespace-a-server-b_get_status",
			))
			g.Expect(tools).NotTo(ContainElement("namespace-b-server-c_echo"))
			g.Expect(tools).NotTo(ContainElement("namespace-b-server-c_get_info"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "namespace-a aggregate tools did not match")

		By("listing tools from namespace-b aggregate endpoint")
		Eventually(func(g Gomega) {
			tools := fetchTools(g, "/namespace-b/mcp")
			g.Expect(tools).To(ContainElements(
				"echo",
				"get_info",
			))
			g.Expect(tools).NotTo(ContainElement("namespace-a-server-a_echo"))
			g.Expect(tools).NotTo(ContainElement("namespace-a-server-b_echo"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "namespace-b aggregate tools did not match")
	})

	It("should aggregate all servers via /mcp", func() {
		By("listing tools from the root aggregate endpoint")
		Eventually(func(g Gomega) {
			tools := fetchTools(g, "/mcp")
			g.Expect(tools).To(ContainElements(
				"namespace-a-server-a_echo",
				"namespace-a-server-a_get_weather",
				"namespace-a-server-b_echo",
				"namespace-a-server-b_get_status",
				"namespace-b-server-c_echo",
				"namespace-b-server-c_get_info",
			))
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "root aggregate tools did not match")
	})
})
