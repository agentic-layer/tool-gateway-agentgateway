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
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GetRequestWithStatus sends a GET request and returns the response body, status code, and error.
// This function does not treat non-200 status codes as errors,
// allowing callers to explicitly check for specific status codes like 404.
func GetRequestWithStatus(url string) ([]byte, int, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	return requestWithStatus(req)
}

// PostRequestWithStatus sends a POST request with a JSON payload and returns the response body, status code, and error.
// This function does not treat non-200 status codes as errors,
// allowing callers to explicitly check for specific status codes.
func PostRequestWithStatus(url string, payload any) ([]byte, int, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// MCP Streamable HTTP transport requires the client to accept both JSON and SSE
	// (agentgateway enforces this and responds in SSE format).
	req.Header.Set("Accept", "application/json, text/event-stream")

	return requestWithStatus(req)
}

// ParseSSEBody extracts JSON payloads from an SSE response body.
// It returns the concatenated JSON from all "data: ..." lines, or the original body
// if no SSE data lines are found (plain JSON response).
func ParseSSEBody(body []byte) []byte {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	var jsonLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			jsonLines = append(jsonLines, strings.TrimPrefix(line, "data: "))
		}
	}
	if len(jsonLines) == 0 {
		return body
	}
	// Return the last data line (final MCP response)
	return []byte(jsonLines[len(jsonLines)-1])
}

// requestWithStatus executes an HTTP request and returns the response body, status code, and error.
func requestWithStatus(req *http.Request) ([]byte, int, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer func(Body io.ReadCloser) {
		if err := Body.Close(); err != nil {
			fmt.Printf("Failed to close response body: %v\n", err)
		}
	}(resp.Body)

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}

	return bodyBytes, resp.StatusCode, nil
}
