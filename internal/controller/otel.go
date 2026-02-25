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
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Standard OpenTelemetry environment variables that should be translated to agentgateway config
// See: https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/
const (
	// General OTLP exporter configuration
	otelExporterOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
	otelExporterOTLPProtocol = "OTEL_EXPORTER_OTLP_PROTOCOL"
	otelExporterOTLPHeaders  = "OTEL_EXPORTER_OTLP_HEADERS"

	// Signal-specific endpoints (these override the general endpoint)
	otelExporterOTLPTracesEndpoint  = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	otelExporterOTLPMetricsEndpoint = "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"
	otelExporterOTLPLogsEndpoint    = "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"

	// Signal-specific protocols (these override the general protocol)
	otelExporterOTLPTracesProtocol  = "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"
	otelExporterOTLPMetricsProtocol = "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"
	otelExporterOTLPLogsProtocol    = "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"

	// Signal-specific headers (these override the general headers)
	otelExporterOTLPTracesHeaders  = "OTEL_EXPORTER_OTLP_TRACES_HEADERS"
	otelExporterOTLPMetricsHeaders = "OTEL_EXPORTER_OTLP_METRICS_HEADERS"
	otelExporterOTLPLogsHeaders    = "OTEL_EXPORTER_OTLP_LOGS_HEADERS"
)

// otelConfig represents the extracted OTEL configuration from environment variables
type otelConfig struct {
	endpoint string
	protocol string
	headers  string

	tracesEndpoint  string
	tracesProtocol  string
	tracesHeaders   string
	metricsEndpoint string
	metricsProtocol string
	metricsHeaders  string
	logsEndpoint    string
	logsProtocol    string
	logsHeaders     string
}

// extractOTELEnvVars extracts OTEL environment variables from the given env var list
// and returns the extracted config and a filtered list of non-OTEL env vars
func extractOTELEnvVars(envVars []corev1.EnvVar) (otelConfig, []corev1.EnvVar) {
	config := otelConfig{}
	filtered := make([]corev1.EnvVar, 0, len(envVars))

	for _, env := range envVars {
		switch env.Name {
		case otelExporterOTLPEndpoint:
			config.endpoint = env.Value
		case otelExporterOTLPProtocol:
			config.protocol = env.Value
		case otelExporterOTLPHeaders:
			config.headers = env.Value
		case otelExporterOTLPTracesEndpoint:
			config.tracesEndpoint = env.Value
		case otelExporterOTLPMetricsEndpoint:
			config.metricsEndpoint = env.Value
		case otelExporterOTLPLogsEndpoint:
			config.logsEndpoint = env.Value
		case otelExporterOTLPTracesProtocol:
			config.tracesProtocol = env.Value
		case otelExporterOTLPMetricsProtocol:
			config.metricsProtocol = env.Value
		case otelExporterOTLPLogsProtocol:
			config.logsProtocol = env.Value
		case otelExporterOTLPTracesHeaders:
			config.tracesHeaders = env.Value
		case otelExporterOTLPMetricsHeaders:
			config.metricsHeaders = env.Value
		case otelExporterOTLPLogsHeaders:
			config.logsHeaders = env.Value
		default:
			// Not an OTEL env var, keep it
			filtered = append(filtered, env)
		}
	}

	return config, filtered
}

// buildTelemetryConfig builds the agentgateway telemetry configuration from OTEL env vars
// Returns nil if no OTEL configuration is present
func buildTelemetryConfig(config otelConfig) map[string]interface{} {
	// Check if any OTEL config is present
	hasConfig := config.endpoint != "" || config.protocol != "" || config.headers != "" ||
		config.tracesEndpoint != "" || config.tracesProtocol != "" || config.tracesHeaders != "" ||
		config.metricsEndpoint != "" || config.metricsProtocol != "" || config.metricsHeaders != "" ||
		config.logsEndpoint != "" || config.logsProtocol != "" || config.logsHeaders != ""

	if !hasConfig {
		return nil
	}

	telemetry := make(map[string]interface{})

	// Build tracing config
	tracing := make(map[string]interface{})

	// Determine the endpoint for tracing (signal-specific overrides general)
	endpoint := config.tracesEndpoint
	if endpoint == "" {
		endpoint = config.endpoint
	}
	if endpoint != "" {
		tracing["otlpEndpoint"] = endpoint
	}

	// Determine the protocol for tracing (signal-specific overrides general)
	protocol := config.tracesProtocol
	if protocol == "" {
		protocol = config.protocol
	}
	if protocol != "" {
		// Normalize protocol value: "grpc/protobuf" or "http/protobuf" -> "grpc" or "http"
		if strings.HasPrefix(protocol, "grpc") {
			tracing["otlpProtocol"] = "grpc"
		} else if strings.HasPrefix(protocol, "http") {
			tracing["otlpProtocol"] = "http"
		} else {
			tracing["otlpProtocol"] = protocol
		}
	}

	// Determine the headers for tracing (signal-specific overrides general)
	headers := config.tracesHeaders
	if headers == "" {
		headers = config.headers
	}
	if headers != "" {
		// Parse headers from OTEL format (comma-separated key=value pairs or JSON)
		// Try JSON first
		var headersMap map[string]interface{}
		if err := json.Unmarshal([]byte(headers), &headersMap); err == nil {
			tracing["headers"] = headersMap
		} else {
			// Parse as comma-separated key=value pairs
			headersMap = parseHeadersString(headers)
			if len(headersMap) > 0 {
				tracing["headers"] = headersMap
			}
		}
	}

	if len(tracing) > 0 {
		telemetry["tracing"] = tracing
	}

	return telemetry
}

// parseHeadersString parses OTEL headers from comma-separated key=value format
// Example: "key1=value1,key2=value2" -> {"key1": "value1", "key2": "value2"}
func parseHeadersString(headers string) map[string]interface{} {
	result := make(map[string]interface{})
	pairs := strings.Split(headers, ",")
	for _, pair := range pairs {
		kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(kv) == 2 {
			result[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return result
}
