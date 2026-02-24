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

package utils

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// MCPInitializeParams are the standard parameters for an MCP initialize request.
var MCPInitializeParams = map[string]interface{}{
	"protocolVersion": "2024-11-05",
	"capabilities":    map[string]interface{}{},
	"clientInfo": map[string]interface{}{
		"name":    "test-client",
		"version": "1.0.0",
	},
}

// FetchTools performs the MCP protocol sequence to list tools from a server:
//  1. initialize → obtain Mcp-Session-Id
//  2. tools/list → pass Mcp-Session-Id header
//
// It returns the sorted list of tool names. Failures are reported via g so
// callers can embed this inside Eventually.
func FetchTools(g gomega.Gomega, target ServiceTarget, path string) []string {
	body, statusCode, err := MakeServiceRequest(
		target,
		func(baseURL string) ([]byte, int, error) {
			// Step 1: initialize (required first per MCP spec)
			initReq := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "initialize",
				"params":  MCPInitializeParams,
			}
			_, initHeaders, initStatus, initErr := PostRequest(baseURL+path, initReq, nil)
			if initErr != nil {
				return nil, initStatus, fmt.Errorf("initialize request failed: %w", initErr)
			}
			if initStatus != 200 {
				return nil, initStatus, fmt.Errorf("initialize returned status %d", initStatus)
			}

			// Step 2: tools/list, forwarding the session ID if the server issued one
			listReq := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      2,
				"method":  "tools/list",
			}
			extraHeaders := map[string]string{}
			if sessionID := initHeaders.Get("Mcp-Session-Id"); sessionID != "" {
				extraHeaders["Mcp-Session-Id"] = sessionID
			}
			listBody, _, listStatus, listErr := PostRequest(baseURL+path, listReq, extraHeaders)
			return listBody, listStatus, listErr
		},
	)
	_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "tools/list at %s: statusCode=%d err=%v\n", path, statusCode, err)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(statusCode).To(gomega.Equal(200))

	var responseMap map[string]interface{}
	g.Expect(json.Unmarshal(ParseSSEBody(body), &responseMap)).To(gomega.Succeed())
	g.Expect(responseMap["jsonrpc"]).To(gomega.Equal("2.0"))
	g.Expect(responseMap).To(gomega.HaveKey("result"))

	result, ok := responseMap["result"].(map[string]interface{})
	g.Expect(ok).To(gomega.BeTrue(), "result should be an object")
	g.Expect(result).To(gomega.HaveKey("tools"))

	tools, ok := result["tools"].([]interface{})
	g.Expect(ok).To(gomega.BeTrue(), "tools should be an array")

	toolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		g.Expect(ok).To(gomega.BeTrue())
		toolNames = append(toolNames, toolMap["name"].(string))
	}
	sort.Strings(toolNames)
	return toolNames
}
