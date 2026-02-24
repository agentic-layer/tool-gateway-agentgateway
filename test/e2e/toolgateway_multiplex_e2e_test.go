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

var _ = Describe("ToolGateway Multiplex MCP", Ordered, func() {

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
		By("applying multiplex E2E test sample")
		_, err := utils.Run(exec.Command("kubectl", "apply",
			"-f", "config/samples/toolgateway_v1alpha1_multiplex_e2e.yaml"))
		Expect(err).NotTo(HaveOccurred(), "Failed to apply multiplex samples")

		By("waiting for ToolServers to be ready")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pods", "-n", "namespace-a",
				"-l", "app.kubernetes.io/name=server-a",
				"-o", "jsonpath={.items[0].status.phase}")
			output, err := cmd.CombinedOutput()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(output)).To(Equal("Running"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pods", "-n", "namespace-a",
				"-l", "app.kubernetes.io/name=server-b",
				"-o", "jsonpath={.items[0].status.phase}")
			output, err := cmd.CombinedOutput()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(output)).To(Equal("Running"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pods", "-n", "namespace-b",
				"-l", "app.kubernetes.io/name=server-c",
				"-o", "jsonpath={.items[0].status.phase}")
			output, err := cmd.CombinedOutput()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(output)).To(Equal("Running"))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())
	})

	AfterAll(func() {
		By("cleaning up multiplex test resources")
		_, _ = utils.Run(exec.Command("kubectl", "delete",
			"-f", "config/samples/toolgateway_v1alpha1_multiplex_e2e.yaml"))
	})

	// Helper function to send MCP tools/list request and extract tool names
	listTools := func(path string) []string {
		mcpRequest := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/list",
		}

		var body []byte
		var statusCode int
		var err error
		Eventually(func(g Gomega) {
			body, statusCode, err = utils.MakeServicePost(
				"tool-gateway", "test-tool-gateway", 80, path, mcpRequest)
			_, _ = fmt.Fprintf(GinkgoWriter,
				"MCP tools/list request to %s: statusCode=%d err=%v body=%s\n",
				path, statusCode, err, string(body))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(statusCode).To(Equal(200))
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), fmt.Sprintf("Failed to send MCP tools/list to %s", path))

		var responseMap map[string]interface{}
		err = json.Unmarshal(utils.ParseSSEBody(body), &responseMap)
		Expect(err).NotTo(HaveOccurred())
		Expect(responseMap["jsonrpc"]).To(Equal("2.0"))
		Expect(responseMap).To(HaveKey("result"))

		result := responseMap["result"].(map[string]interface{})
		Expect(result).To(HaveKey("tools"))

		tools := result["tools"].([]interface{})
		toolNames := make([]string, 0, len(tools))
		for _, tool := range tools {
			toolMap := tool.(map[string]interface{})
			toolNames = append(toolNames, toolMap["name"].(string))
		}

		// Sort for consistent comparison
		sort.Strings(toolNames)
		return toolNames
	}

	It("should expose individual server tools via /<namespace>/<server>/mcp", func() {
		By("listing tools from server-a")
		tools := listTools("/namespace-a/server-a/mcp")
		Expect(tools).To(ContainElements("namespace-a-server-a_echo", "namespace-a-server-a_get_weather"))

		By("listing tools from server-b")
		tools = listTools("/namespace-a/server-b/mcp")
		Expect(tools).To(ContainElements("namespace-a-server-b_echo", "namespace-a-server-b_get_status"))

		By("listing tools from server-c")
		tools = listTools("/namespace-b/server-c/mcp")
		Expect(tools).To(ContainElements("namespace-b-server-c_echo", "namespace-b-server-c_get_info"))
	})

	It("should multiplex all namespace-a servers via /namespace-a/mcp", func() {
		By("listing tools from namespace-a multiplex endpoint")
		tools := listTools("/namespace-a/mcp")

		// Should contain tools from both server-a and server-b in namespace-a
		Expect(tools).To(ContainElements(
			"namespace-a-server-a_echo",
			"namespace-a-server-a_get_weather",
			"namespace-a-server-b_echo",
			"namespace-a-server-b_get_status",
		))

		// Should NOT contain tools from server-c in namespace-b
		Expect(tools).NotTo(ContainElement("namespace-b-server-c_echo"))
		Expect(tools).NotTo(ContainElement("namespace-b-server-c_get_info"))
	})

	It("should multiplex all namespace-b servers via /namespace-b/mcp", func() {
		By("listing tools from namespace-b multiplex endpoint")
		tools := listTools("/namespace-b/mcp")

		// Should contain tools from server-c in namespace-b
		Expect(tools).To(ContainElements(
			"namespace-b-server-c_echo",
			"namespace-b-server-c_get_info",
		))

		// Should NOT contain tools from namespace-a servers
		Expect(tools).NotTo(ContainElement("namespace-a-server-a_echo"))
		Expect(tools).NotTo(ContainElement("namespace-a-server-b_echo"))
	})

	It("should multiplex all servers via /mcp", func() {
		By("listing tools from root multiplex endpoint")
		tools := listTools("/mcp")

		// Should contain tools from all three servers
		Expect(tools).To(ContainElements(
			"namespace-a-server-a_echo",
			"namespace-a-server-a_get_weather",
			"namespace-a-server-b_echo",
			"namespace-a-server-b_get_status",
			"namespace-b-server-c_echo",
			"namespace-b-server-c_get_info",
		))
	})
})
