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
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Collecting controller logs for debugging")
			cmd := exec.Command("kubectl", "logs", "-n", "tool-gateway-agentgateway-system",
				"-l", "control-plane=controller-manager", "--tail=100")
			output, _ := cmd.CombinedOutput()
			GinkgoWriter.Printf("Controller logs:\n%s\n", string(output))
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

			By("listing tools from server-c")
			Eventually(func(g Gomega) {
				tools := utils.FetchTools(g, toolGateway, "/namespace-b/server-c/mcp")
				g.Expect(tools).To(Equal([]string{"echo", "get_info"}))
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "tools from server-c did not match")
		})
	})

	Describe("namespace aggregate endpoint", func() {
		It("should aggregate all servers in a namespace via /<namespace>/mcp", func() {
			By("listing tools from namespace-a aggregate endpoint")
			Eventually(func(g Gomega) {
				tools := utils.FetchTools(g, toolGateway, "/namespace-a/mcp")
				g.Expect(tools).To(Equal([]string{
					"namespace-a-server-a_echo",
					"namespace-a-server-a_get_weather",
					"namespace-a-server-b_echo",
					"namespace-a-server-b_get_status",
				}))
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "namespace-a aggregate tools did not match")

			By("listing tools from namespace-b aggregate endpoint")
			Eventually(func(g Gomega) {
				tools := utils.FetchTools(g, toolGateway, "/namespace-b/mcp")
				g.Expect(tools).To(Equal([]string{
					"echo",
					"get_info",
				}))
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "namespace-b aggregate tools did not match")
		})
	})

	Describe("root aggregate endpoint", func() {
		It("should aggregate all servers via /mcp", func() {
			By("listing tools from the root aggregate endpoint")
			Eventually(func(g Gomega) {
				tools := utils.FetchTools(g, toolGateway, "/mcp")
				g.Expect(tools).To(Equal([]string{
					"namespace-a-server-a_echo",
					"namespace-a-server-a_get_weather",
					"namespace-a-server-b_echo",
					"namespace-a-server-b_get_status",
					"namespace-b-server-c_echo",
					"namespace-b-server-c_get_info",
				}))
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "root aggregate tools did not match")
		})
	})
})
