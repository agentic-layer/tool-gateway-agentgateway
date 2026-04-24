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
// agentgateway expresses it as CEL rules in AgentgatewayPolicy.spec.backend.mcpAuthorization.rules.
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
// into AgentgatewayPolicy.spec.backend.mcpAuthorization.rules.
// Returns nil when filter is nil or when allow and deny are both empty.
func buildMcpAuthorizationRules(filter *agentruntimev1alpha1.ToolFilter) []string {
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

	switch {
	case len(allowPreds) > 0 && len(denyPreds) > 0:
		return []string{"(" + orJoin(allowPreds) + ") && !(" + orJoin(denyPreds) + ")"}
	case len(allowPreds) > 0:
		return []string{orJoin(allowPreds)}
	case len(denyPreds) > 0:
		return []string{"!(" + orJoin(denyPreds) + ")"}
	default:
		return nil
	}
}
