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
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/agentic-layer/tool-gateway-kgateway/test/utils"
)

var _ = Describe("ToolGateway", Ordered, func() {

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Collecting controller logs for debugging")
			cmd := exec.Command("kubectl", "logs", "-n", "tool-gateway-kgateway-system",
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

	It("should proxy MCP requests to tool server", func() {
		By("sending MCP initialize request to the gateway")
		mcpRequest := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params": map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{},
				"clientInfo": map[string]interface{}{
					"name":    "test-client",
					"version": "1.0.0",
				},
			},
		}

		var body []byte
		Eventually(func(g Gomega) {
			var statusCode int
			var err error
			// kgateway service in agentgateway-system namespace handles all Gateway traffic
			// Using port 9977 which is the kgateway HTTP port
			body, statusCode, err = utils.MakeServicePost("tool-gateway", "test-tool-gateway", 80,
				"/test/echo-mcp-server/mcp", mcpRequest)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(statusCode).To(Equal(200))
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "Failed to send MCP request to gateway")

		By("verifying MCP response")
		var responseMap map[string]interface{}
		err := json.Unmarshal(body, &responseMap)
		Expect(err).NotTo(HaveOccurred())
		Expect(responseMap["jsonrpc"]).To(Equal("2.0"))
		Expect(responseMap["id"]).To(BeEquivalentTo(1))
		Expect(responseMap).To(HaveKey("result"))
	})
})
