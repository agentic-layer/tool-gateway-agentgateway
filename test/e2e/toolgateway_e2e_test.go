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
	"context"
	"errors"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/agentic-layer/tool-gateway-agentgateway/test/utils"
)

const toolGatewaySampleFile = "config/samples/toolgateway_v1alpha1_toolgateway_with_toolserver.yaml"

var toolGateway = utils.ServiceTarget{
	Namespace:   "tool-gateway",
	ServiceName: "test-tool-gateway",
	Port:        80,
}

var _ = Describe("ToolGateway", func() {

	BeforeEach(func() {
		By("applying ToolGateway with ToolServer sample")
		_, err := utils.Run(exec.Command("kubectl", "apply", "-f", toolGatewaySampleFile))
		Expect(err).NotTo(HaveOccurred(), "Failed to apply samples")

		By("waiting for gateway service to have running pods")
		Eventually(func() error {
			return utils.WaitForServiceReady(context.Background(), toolGateway)
		}, 3*time.Minute, 5*time.Second).Should(Succeed(), "gateway service did not become ready")
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			fetchControllerManagerPodLogs()
			fetchKubernetesEvents()
			utils.CollectDiagnostics("toolgateway-e2e")
		}

		By("cleaning up test resources")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "-f", toolGatewaySampleFile))
	})

	Describe("individual server endpoints", func() {
		It("should expose individual server tools via /<namespace>/<server>/mcp", func() {
			By("listing tools from server-a")
			Eventually(func(g Gomega) {
				tools := utils.FetchTools(g, toolGateway, "/namespace-a/server-a/mcp")
				g.Expect(tools).To(Equal([]string{"echo", "get_weather"}))
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "tools from server-a did not match")

			By("listing tools from server-b")
			Eventually(func(g Gomega) {
				tools := utils.FetchTools(g, toolGateway, "/namespace-a/server-b/mcp")
				g.Expect(tools).To(Equal([]string{"echo", "get_status"}))
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "tools from server-b did not match")

			By("listing tools from server-c (get_info is filtered out via ToolRoute.spec.toolFilter)")
			Eventually(func(g Gomega) {
				tools := utils.FetchTools(g, toolGateway, "/namespace-b/server-c/mcp")
				g.Expect(tools).To(Equal([]string{"echo"}))
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "tools from server-c did not match")
		})
	})

	Describe("tool invocation", func() {
		It("should echo a message via tools/call on /<namespace>/<server>/mcp", func() {
			By("invoking the echo tool on server-a with a plain message")
			Eventually(func(g Gomega) {
				echoed, err := utils.CallTool(g, toolGateway, "/namespace-a/server-a/mcp", "echo",
					map[string]interface{}{"message": "hello from e2e test"})
				g.Expect(err).NotTo(HaveOccurred(), "echo tool should not be rejected")
				g.Expect(echoed).To(ContainSubstring("hello from e2e test"),
					"echo tool should return the message verbatim")
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "echo tool did not respond")
		})
	})

	Describe("tool filter", func() {
		It("should hide tools matched by ToolRoute.spec.toolFilter on the per-route endpoint", func() {
			By("verifying server-c's individual endpoint does not expose the denied get_info tool")
			Eventually(func(g Gomega) {
				tools := utils.FetchTools(g, toolGateway, "/namespace-b/server-c/mcp")
				g.Expect(tools).NotTo(ContainElement("get_info"),
					"get_info is denied by toolFilter and must not appear in tools/list")
				g.Expect(tools).To(ContainElement("echo"),
					"non-filtered tools must still be exposed")
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "server-c filter not applied")

			By("verifying tools/call to the denied tool on the per-route endpoint is rejected")
			Eventually(func(g Gomega) {
				_, err := utils.CallTool(g, toolGateway, "/namespace-b/server-c/mcp", "get_info",
					map[string]interface{}{})
				var rejected *utils.ToolCallRejected
				g.Expect(errors.As(err, &rejected)).To(BeTrue(),
					"expected gateway rejection, got: %v", err)
				g.Expect(rejected.RPCError).NotTo(BeNil(),
					"denied tool should be rejected via JSON-RPC error, got: %+v", rejected)
				g.Expect(rejected.RPCError["message"]).To(ContainSubstring("Unknown tool"),
					"denied tool should be rejected as if unknown, got: %v", rejected.RPCError)
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "filtered tool call should have been rejected")

		})

	})
})
