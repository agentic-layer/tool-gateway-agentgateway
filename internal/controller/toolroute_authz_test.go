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

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

var _ = Describe("globToCELPredicate", func() {
	It("compiles a plain name to an equality check", func() {
		Expect(globToCELPredicate("search_issues")).
			To(Equal(`mcp.tool.name == "search_issues"`))
	})

	It("compiles a glob with trailing * to a regex matches()", func() {
		Expect(globToCELPredicate("get_*")).
			To(Equal(`mcp.tool.name.matches("^get_.*$")`))
	})

	It("compiles a glob with surrounding * to a regex matches()", func() {
		Expect(globToCELPredicate("*delete*")).
			To(Equal(`mcp.tool.name.matches("^.*delete.*$")`))
	})

	It("treats ? as a single-char wildcard", func() {
		Expect(globToCELPredicate("do?")).
			To(Equal(`mcp.tool.name.matches("^do.$")`))
	})

	It("escapes regex metacharacters in a plain-looking name", func() {
		Expect(globToCELPredicate("a.b")).
			To(Equal(`mcp.tool.name == "a.b"`))
		Expect(globToCELPredicate("a.b*")).
			To(Equal(`mcp.tool.name.matches("^a\\.b.*$")`))
	})
})

var _ = Describe("buildMcpAuthorizationRules", func() {
	It("returns empty when filter is nil", func() {
		Expect(buildMcpAuthorizationRules(nil)).To(BeEmpty())
	})

	It("returns empty when both allow and deny are empty", func() {
		Expect(buildMcpAuthorizationRules(&agentruntimev1alpha1.ToolFilter{})).To(BeEmpty())
	})

	It("returns a single rule ORing allow patterns when only allow is set", func() {
		rules := buildMcpAuthorizationRules(&agentruntimev1alpha1.ToolFilter{
			Allow: []string{"get_*", "list_*"},
		})
		Expect(rules).To(ConsistOf(
			`mcp.tool.name.matches("^get_.*$") || mcp.tool.name.matches("^list_.*$")`,
		))
	})

	It("returns a rule negating deny patterns when only deny is set", func() {
		rules := buildMcpAuthorizationRules(&agentruntimev1alpha1.ToolFilter{
			Deny: []string{"*delete*"},
		})
		Expect(rules).To(ConsistOf(
			`!(mcp.tool.name.matches("^.*delete.*$"))`,
		))
	})

	It("ANDs allow and (negated) deny when both are set", func() {
		rules := buildMcpAuthorizationRules(&agentruntimev1alpha1.ToolFilter{
			Allow: []string{"get_*"},
			Deny:  []string{"force_push"},
		})
		Expect(rules).To(ConsistOf(
			`(mcp.tool.name.matches("^get_.*$")) && !(mcp.tool.name == "force_push")`,
		))
	})
})
