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
	"net/http"
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

// maxBodyPreview caps how many bytes of a response body we include in error messages.
const maxBodyPreview = 1024

// previewBody truncates a body for inclusion in error messages and Ginkgo logs.
func previewBody(body []byte) string {
	if len(body) <= maxBodyPreview {
		return string(body)
	}
	return fmt.Sprintf("%s…[truncated %d bytes]", body[:maxBodyPreview], len(body)-maxBodyPreview)
}

// doMCPRequest sends a JSON-RPC request and returns the response body + session ID
// issued by the server (if any). On non-200 status it returns an error whose
// message includes the URL, status, and a preview of the response body so failed
// tests can be debugged from Ginkgo output alone.
func doMCPRequest(
	url string, payload map[string]interface{}, extraHeaders map[string]string,
) (body []byte, headers http.Header, err error) {
	reqJSON, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "MCP → POST %s body=%s headers=%v\n", url, reqJSON, extraHeaders)

	body, headers, status, err := PostRequest(url, payload, extraHeaders)
	if err != nil {
		return nil, nil, fmt.Errorf("POST %s failed: %w", url, err)
	}
	_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "MCP ← %s status=%d headers=%v body=%s\n",
		url, status, headers, previewBody(body))
	if status != 200 {
		method, _ := payload["method"].(string)
		return nil, headers, fmt.Errorf(
			"MCP %s to %s returned status %d (Content-Type=%q): body=%s",
			method, url, status, headers.Get("Content-Type"), previewBody(body))
	}
	return body, headers, nil
}

// initializeMCPSession sends an MCP initialize request and returns the Mcp-Session-Id
// header (empty string if none was issued).
func initializeMCPSession(url string) (sessionID string, err error) {
	initReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  MCPInitializeParams,
	}
	_, headers, err := doMCPRequest(url, initReq, nil)
	if err != nil {
		return "", err
	}
	return headers.Get("Mcp-Session-Id"), nil
}

// sessionHeaders returns the extra-headers map to send with follow-up MCP requests.
func sessionHeaders(sessionID string) map[string]string {
	if sessionID == "" {
		return nil
	}
	return map[string]string{"Mcp-Session-Id": sessionID}
}

// CallTool performs the MCP protocol sequence to invoke a single tool:
//  1. initialize → obtain Mcp-Session-Id
//  2. tools/call → pass Mcp-Session-Id header
//
// It returns the concatenated text content of the tool result. Failures are
// reported via g so callers can embed this inside Eventually.
func CallTool(g gomega.Gomega, target ServiceTarget, path, toolName string, arguments map[string]interface{}) string {
	body, _, err := MakeServiceRequest(
		target,
		func(baseURL string) ([]byte, int, error) {
			url := baseURL + path

			sessionID, err := initializeMCPSession(url)
			if err != nil {
				return nil, 0, err
			}

			callReq := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      2,
				"method":  "tools/call",
				"params": map[string]interface{}{
					"name":      toolName,
					"arguments": arguments,
				},
			}
			callBody, _, callErr := doMCPRequest(url, callReq, sessionHeaders(sessionID))
			if callErr != nil {
				return callBody, 0, callErr
			}
			return callBody, 200, nil
		},
	)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "tools/call %q at %s failed", toolName, path)

	var responseMap map[string]interface{}
	parsedBody := ParseSSEBody(body)
	g.Expect(json.Unmarshal(parsedBody, &responseMap)).To(gomega.Succeed(),
		"tools/call response is not valid JSON: %s", previewBody(parsedBody))
	g.Expect(responseMap["jsonrpc"]).To(gomega.Equal("2.0"))
	if rpcErr, ok := responseMap["error"]; ok {
		g.Expect(rpcErr).To(gomega.BeNil(), "tools/call returned JSON-RPC error: %v", rpcErr)
	}
	g.Expect(responseMap).To(gomega.HaveKey("result"))

	result, ok := responseMap["result"].(map[string]interface{})
	g.Expect(ok).To(gomega.BeTrue(), "result should be an object, got: %v", responseMap["result"])

	contentItems, ok := result["content"].([]interface{})
	g.Expect(ok).To(gomega.BeTrue(), "result.content should be an array, got: %v", result["content"])

	var text string
	for _, item := range contentItems {
		itemMap, ok := item.(map[string]interface{})
		g.Expect(ok).To(gomega.BeTrue(), "content item should be an object, got: %v", item)
		if t, ok := itemMap["text"].(string); ok {
			text += t
		}
	}
	return text
}

// FetchTools performs the MCP protocol sequence to list tools from a server:
//  1. initialize → obtain Mcp-Session-Id
//  2. tools/list → pass Mcp-Session-Id header
//
// It returns the sorted list of tool names. Failures are reported via g so
// callers can embed this inside Eventually.
func FetchTools(g gomega.Gomega, target ServiceTarget, path string) []string {
	body, _, err := MakeServiceRequest(
		target,
		func(baseURL string) ([]byte, int, error) {
			url := baseURL + path

			sessionID, err := initializeMCPSession(url)
			if err != nil {
				return nil, 0, err
			}

			listReq := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      2,
				"method":  "tools/list",
			}
			listBody, _, listErr := doMCPRequest(url, listReq, sessionHeaders(sessionID))
			if listErr != nil {
				return listBody, 0, listErr
			}
			return listBody, 200, nil
		},
	)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "tools/list at %s failed", path)

	var responseMap map[string]interface{}
	parsedBody := ParseSSEBody(body)
	g.Expect(json.Unmarshal(parsedBody, &responseMap)).To(gomega.Succeed(),
		"tools/list response is not valid JSON: %s", previewBody(parsedBody))
	g.Expect(responseMap["jsonrpc"]).To(gomega.Equal("2.0"))
	if rpcErr, ok := responseMap["error"]; ok {
		g.Expect(rpcErr).To(gomega.BeNil(), "tools/list returned JSON-RPC error: %v", rpcErr)
	}
	g.Expect(responseMap).To(gomega.HaveKey("result"))

	result, ok := responseMap["result"].(map[string]interface{})
	g.Expect(ok).To(gomega.BeTrue(), "result should be an object, got: %v", responseMap["result"])
	g.Expect(result).To(gomega.HaveKey("tools"))

	tools, ok := result["tools"].([]interface{})
	g.Expect(ok).To(gomega.BeTrue(), "tools should be an array, got: %v", result["tools"])

	toolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		g.Expect(ok).To(gomega.BeTrue())
		toolNames = append(toolNames, toolMap["name"].(string))
	}
	sort.Strings(toolNames)
	return toolNames
}
