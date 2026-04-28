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

// Package-local tool-filter translation: our CRD expresses filtering as glob allow/deny.
// agentgateway expresses it as CEL rules in AgentgatewayPolicy.spec.backend.mcp.authorization.policy.matchExpressions.
// This file builds those CEL rules.
package controller

import (
	"regexp"
	"strings"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

// hasGlobMeta returns true if the pattern contains '*' or '?'.
func hasGlobMeta(p string) bool {
	return strings.ContainsAny(p, "*?")
}

// globToRegex converts a glob (* and ?) into an anchored regex.
// Regex metacharacters in non-wildcard segments are escaped via regexp.QuoteMeta.
func globToRegex(glob string) string {
	var b strings.Builder
	b.WriteString("^")
	var literal strings.Builder
	flushLiteral := func() {
		if literal.Len() > 0 {
			b.WriteString(regexp.QuoteMeta(literal.String()))
			literal.Reset()
		}
	}
	for _, r := range glob {
		switch r {
		case '*':
			flushLiteral()
			b.WriteString(".*")
		case '?':
			flushLiteral()
			b.WriteString(".")
		default:
			literal.WriteRune(r)
		}
	}
	flushLiteral()
	b.WriteString("$")
	return b.String()
}

// celEscape quotes a string as a CEL string literal, escaping backslashes and quotes.
func celEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// globToCELPredicate compiles a single glob pattern to a CEL boolean expression over mcp.tool.name.
// Plain names (no glob metas) compile to an equality check; patterns with * or ? use matches().
func globToCELPredicate(pattern string) string {
	if !hasGlobMeta(pattern) {
		return "mcp.tool.name == " + celEscape(pattern)
	}
	return "mcp.tool.name.matches(" + celEscape(globToRegex(pattern)) + ")"
}

// orJoin joins a list of CEL predicates with `||`.
func orJoin(preds []string) string {
	return strings.Join(preds, " || ")
}

// buildMcpAuthorizationRules converts a ToolFilter into the list of CEL rules to write
// into AgentgatewayPolicy.spec.backend.mcp.authorization.policy.matchExpressions.
// Returns nil when filter is nil or when allow and deny are both empty.
func buildMcpAuthorizationRules(filter *agentruntimev1alpha1.ToolFilter) []string {
	return buildMcpAuthorizationRulesScopedToTarget(filter, "")
}

// buildMcpAuthorizationRulesScopedToTarget builds the same rules as buildMcpAuthorizationRules
// but scopes them to a specific multiplex target (matched via `mcp.tool.target`). Tool
// patterns are matched against `mcp.tool.name`, which agentgateway evaluates against the
// resolved upstream tool name *without* the multiplex prefix; the scoping is therefore
// done with the target identity, not by string-prefixing the patterns. When target is empty
// the rule applies unconditionally; when non-empty the rule short-circuits to true for tools
// belonging to other targets so they remain governed by their own filter (or none at all).
func buildMcpAuthorizationRulesScopedToTarget(filter *agentruntimev1alpha1.ToolFilter, target string) []string {
	if filter == nil || (len(filter.Allow) == 0 && len(filter.Deny) == 0) {
		return nil
	}

	var allowPreds []string
	for _, p := range filter.Allow {
		allowPreds = append(allowPreds, globToCELPredicate(p))
	}
	var denyPreds []string
	for _, p := range filter.Deny {
		denyPreds = append(denyPreds, globToCELPredicate(p))
	}

	var inner string
	switch {
	case len(allowPreds) > 0 && len(denyPreds) > 0:
		inner = "(" + orJoin(allowPreds) + ") && !(" + orJoin(denyPreds) + ")"
	case len(allowPreds) > 0:
		inner = orJoin(allowPreds)
	case len(denyPreds) > 0:
		inner = "!(" + orJoin(denyPreds) + ")"
	default:
		return nil
	}

	if target == "" {
		return []string{inner}
	}
	return []string{"mcp.tool.target != " + celEscape(target) + " || (" + inner + ")"}
}

// MultiplexFilterContribution carries one ToolRoute's filter into the multiplex
// authorization aggregator together with the multiplex target name that
// identifies its tools at request time via `mcp.tool.target`. Target is empty
// when the rule should apply unconditionally (e.g., a single-target multiplex
// where scoping is unnecessary).
type MultiplexFilterContribution struct {
	Target string
	Filter *agentruntimev1alpha1.ToolFilter
}

// buildMultiplexAuthorizationRules combines per-route filters into the
// matchExpressions for an AgentgatewayPolicy attached to a multiplex HTTPRoute.
// All contributions are AND'd together so a tool must satisfy every contributing
// route's filter (most filters short-circuit to true for tools not in their
// prefix). Returns nil when no contribution has a non-empty filter.
func buildMultiplexAuthorizationRules(contributions []MultiplexFilterContribution) []string {
	clauses := make([]string, 0, len(contributions))
	for _, c := range contributions {
		clauses = append(clauses, buildMcpAuthorizationRulesScopedToTarget(c.Filter, c.Target)...)
	}
	if len(clauses) == 0 {
		return nil
	}
	if len(clauses) == 1 {
		return clauses
	}
	parts := make([]string, len(clauses))
	for i, c := range clauses {
		parts[i] = "(" + c + ")"
	}
	return []string{strings.Join(parts, " && ")}
}
