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
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/agentic-layer/tool-gateway-agentgateway/test/utils"
)

var _ = Describe("ToolRoute", func() {

	BeforeEach(func() {
		By("applying ToolGateway + ToolServer + ToolRoute sample")
		_, err := utils.Run(exec.Command("kubectl", "apply", "-f", toolGatewaySampleFile))
		Expect(err).NotTo(HaveOccurred(), "Failed to apply samples")
	})

	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Collecting controller logs for debugging")
			cmd := exec.Command("kubectl", "logs", "-n", "tool-gateway-agentgateway-system",
				"-l", "control-plane=controller-manager", "--tail=200")
			output, _ := cmd.CombinedOutput()
			GinkgoWriter.Printf("Controller logs:\n%s\n", string(output))
		}

		By("cleaning up test resources")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "-f", toolGatewaySampleFile))
	})

	Describe("per-route Gateway resources", func() {
		It("creates an AgentgatewayBackend per ToolRoute targeting the backing ToolServer", func() {
			By("verifying the AgentgatewayBackend exists for the server-a route")
			Eventually(func(g Gomega) {
				output, err := utils.Run(exec.Command("kubectl", "get", "agentgatewaybackends",
					"-n", "namespace-a",
					"test-tool-gateway-server-a",
					"-o", "jsonpath={.spec.mcp.targets[0].static.host}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).To(Equal("server-a.namespace-a.svc.cluster.local"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("creates an HTTPRoute per ToolRoute parented to the ToolGateway", func() {
			By("verifying the HTTPRoute exists for the server-a route and is parented to the ToolGateway")
			Eventually(func(g Gomega) {
				output, err := utils.Run(exec.Command("kubectl", "get", "httproutes",
					"-n", "namespace-a",
					"test-tool-gateway-server-a",
					"-o", "jsonpath={.spec.parentRefs[0].name}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).To(Equal("test-tool-gateway"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		It("populates ToolRoute.status.url with the gateway-local URL", func() {
			By("verifying status.url ends with /namespace-a/server-a/mcp")
			Eventually(func(g Gomega) {
				output, err := utils.Run(exec.Command("kubectl", "get", "toolroute",
					"-n", "namespace-a",
					"server-a",
					"-o", "jsonpath={.status.url}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).To(HaveSuffix("/namespace-a/server-a/mcp"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())
		})
	})
})
