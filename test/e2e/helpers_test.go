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
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/agentic-layer/tool-gateway-agentgateway/test/utils"
)

// verifyToolFilterBehavior asserts that the denied tool "get_info" is hidden from
// tools/list and that calling it is rejected with a JSON-RPC error, while the
// non-filtered "echo" tool remains visible. It is shared between the plain and
// guardrail-enabled ToolGateway test suites.
func verifyToolFilterBehavior(gateway utils.ServiceTarget, path string) {
	By("verifying the individual endpoint does not expose the denied get_info tool")
	Eventually(func(g Gomega) {
		tools := utils.FetchTools(g, gateway, path)
		g.Expect(tools).NotTo(ContainElement("get_info"),
			"get_info is denied by toolFilter and must not appear in tools/list")
		g.Expect(tools).To(ContainElement("echo"),
			"non-filtered tools must still be exposed")
	}, 2*time.Minute, 5*time.Second).Should(Succeed(), "filter not applied")

	By("verifying tools/call to the denied tool on the per-route endpoint is rejected")
	Eventually(func(g Gomega) {
		_, err := utils.CallTool(g, gateway, path, "get_info", map[string]interface{}{})
		var rejected *utils.ToolCallRejected
		g.Expect(errors.As(err, &rejected)).To(BeTrue(),
			"expected gateway rejection, got: %v", err)
		g.Expect(rejected.RPCError).NotTo(BeNil(),
			"denied tool should be rejected via JSON-RPC error, got: %+v", rejected)
		g.Expect(rejected.RPCError["message"]).To(ContainSubstring("Unknown tool"),
			"denied tool should be rejected as if unknown, got: %v", rejected.RPCError)
	}, 2*time.Minute, 5*time.Second).Should(Succeed(), "filtered tool call should have been rejected")
}
