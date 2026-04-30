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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/agentic-layer/tool-gateway-agentgateway/test/utils"
)

const guardrailsKustomizeDir = "config/samples/guardrails"

var guardrailsGateway = utils.ServiceTarget{
	Namespace:   "default",
	ServiceName: "test-tool-gateway",
	Port:        80,
}

var _ = Describe("ToolGateway with Guardrails", Ordered, func() {

	BeforeAll(func() {
		By("applying guardrails kustomization (Presidio, ToolGateway, Guard, ToolServer)")
		_, err := utils.Run(exec.Command("kubectl", "apply", "-k", guardrailsKustomizeDir))
		Expect(err).NotTo(HaveOccurred(), "Failed to apply guardrails kustomization")
	})

	AfterAll(func() {
		By("removing guardrails kustomization")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "-k", guardrailsKustomizeDir, "--ignore-not-found=true"))
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			fetchControllerManagerPodLogs()
			fetchKubernetesEvents()
			utils.CollectDiagnostics("guardrails-e2e")
		}
	})

	Describe("individual server endpoints", func() {
		It("should expose individual server tools via /<namespace>/<server>/mcp", func() {
			By("listing tools from echo-server")
			Eventually(func(g Gomega) {
				tools := utils.FetchTools(g, guardrailsGateway, "/default/echo-server/mcp")
				g.Expect(tools).To(Equal([]string{"echo", "get_weather"}))
			}, 5*time.Minute, 10*time.Second).Should(Succeed(), "tools from echo-server did not match")

			By("listing tools from filtered-server (get_info is filtered out via ToolRoute.spec.toolFilter)")
			Eventually(func(g Gomega) {
				tools := utils.FetchTools(g, guardrailsGateway, "/default/filtered-server/mcp")
				g.Expect(tools).To(Equal([]string{"echo"}))
			}, 2*time.Minute, 5*time.Second).Should(Succeed(), "tools from filtered-server did not match")
		})
	})

	Describe("tool invocation", func() {
		It("should mask PII in MCP tool responses routed through the guarded gateway", func() {
			const piiMessage = "My name is John Smith and my email is john.smith@example.com."

			By("calling the echo tool with PII content via the guarded gateway")
			var echoed string
			Eventually(func(g Gomega) {
				result, err := utils.CallTool(g, guardrailsGateway, "/default/echo-server/mcp", "echo",
					map[string]interface{}{"message": piiMessage})
				g.Expect(err).NotTo(HaveOccurred(), "echo tool should not be rejected")
				g.Expect(result).NotTo(BeEmpty(), "echo tool returned empty content")
				echoed = result
			}, 3*time.Minute, 10*time.Second).Should(Succeed(), "echo tool did not return a response")

			By("verifying PII is masked with Presidio placeholders in the response")
			Expect(echoed).To(MatchRegexp(`<PERSON[^>]*>`),
				"name should be replaced with <PERSON> placeholder")
			Expect(echoed).To(MatchRegexp(`<EMAIL_ADDRESS[^>]*>`),
				"email should be replaced with <EMAIL_ADDRESS> placeholder")
			Expect(echoed).NotTo(ContainSubstring("John Smith"),
				"original name must not be present in the response")
			Expect(echoed).NotTo(ContainSubstring("john.smith@example.com"),
				"original email must not be present in the response")
		})
	})

	Describe("tool filter", func() {
		It("should hide tools matched by ToolRoute.spec.toolFilter on the per-route endpoint", func() {
			verifyToolFilterBehavior(guardrailsGateway, "/default/filtered-server/mcp")
		})
	})
})
