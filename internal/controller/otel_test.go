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
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/stretchr/testify/assert"
)

func TestExtractOTELEnvVars(t *testing.T) {
	tests := []struct {
		name             string
		input            []corev1.EnvVar
		expectedConfig   otelConfig
		expectedFiltered []corev1.EnvVar
	}{
		{
			name: "no OTEL env vars",
			input: []corev1.EnvVar{
				{Name: "FOO", Value: "bar"},
				{Name: "BAZ", Value: "qux"},
			},
			expectedConfig: otelConfig{},
			expectedFiltered: []corev1.EnvVar{
				{Name: "FOO", Value: "bar"},
				{Name: "BAZ", Value: "qux"},
			},
		},
		{
			name: "general OTEL endpoint only",
			input: []corev1.EnvVar{
				{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://otel-collector:4318"},
				{Name: "FOO", Value: "bar"},
			},
			expectedConfig: otelConfig{
				endpoint: "http://otel-collector:4318",
			},
			expectedFiltered: []corev1.EnvVar{
				{Name: "FOO", Value: "bar"},
			},
		},
		{
			name: "general OTEL config with protocol and headers",
			input: []corev1.EnvVar{
				{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://otel-collector:4318"},
				{Name: "OTEL_EXPORTER_OTLP_PROTOCOL", Value: "http/protobuf"},
				{Name: "OTEL_EXPORTER_OTLP_HEADERS", Value: "key1=value1,key2=value2"},
				{Name: "OTHER_ENV", Value: "value"},
			},
			expectedConfig: otelConfig{
				endpoint: "http://otel-collector:4318",
				protocol: "http/protobuf",
				headers:  "key1=value1,key2=value2",
			},
			expectedFiltered: []corev1.EnvVar{
				{Name: "OTHER_ENV", Value: "value"},
			},
		},
		{
			name: "signal-specific OTEL config",
			input: []corev1.EnvVar{
				{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://otel-collector:4318"},
				{Name: "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", Value: "http://traces-collector:4318"},
				{Name: "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", Value: "grpc"},
				{Name: "OTEL_EXPORTER_OTLP_TRACES_HEADERS", Value: "trace-key=trace-value"},
			},
			expectedConfig: otelConfig{
				endpoint:       "http://otel-collector:4318",
				tracesEndpoint: "http://traces-collector:4318",
				tracesProtocol: "grpc",
				tracesHeaders:  "trace-key=trace-value",
			},
			expectedFiltered: []corev1.EnvVar{},
		},
		{
			name: "all signal-specific configs",
			input: []corev1.EnvVar{
				{Name: "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", Value: "http://traces:4318"},
				{Name: "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", Value: "http://metrics:4318"},
				{Name: "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", Value: "http://logs:4318"},
				{Name: "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", Value: "grpc"},
				{Name: "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", Value: "http/protobuf"},
				{Name: "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", Value: "http/protobuf"},
			},
			expectedConfig: otelConfig{
				tracesEndpoint:  "http://traces:4318",
				metricsEndpoint: "http://metrics:4318",
				logsEndpoint:    "http://logs:4318",
				tracesProtocol:  "grpc",
				metricsProtocol: "http/protobuf",
				logsProtocol:    "http/protobuf",
			},
			expectedFiltered: []corev1.EnvVar{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, filtered := extractOTELEnvVars(tt.input)
			assert.Equal(t, tt.expectedConfig, config)
			assert.Equal(t, tt.expectedFiltered, filtered)
		})
	}
}

func TestBuildTelemetryConfig(t *testing.T) {
	tests := []struct {
		name     string
		config   otelConfig
		expected map[string]interface{}
	}{
		{
			name:     "empty config",
			config:   otelConfig{},
			expected: nil,
		},
		{
			name: "general endpoint only",
			config: otelConfig{
				endpoint: "http://otel-collector:4318",
			},
			expected: map[string]interface{}{
				"config": map[string]interface{}{
					"tracing": map[string]interface{}{
						"otlpEndpoint": "http://otel-collector:4318",
					},
				},
			},
		},
		{
			name: "endpoint and protocol (grpc/protobuf)",
			config: otelConfig{
				endpoint: "http://otel-collector:4317",
				protocol: "grpc/protobuf",
			},
			expected: map[string]interface{}{
				"config": map[string]interface{}{
					"tracing": map[string]interface{}{
						"otlpEndpoint": "http://otel-collector:4317",
						"otlpProtocol": "grpc",
					},
				},
			},
		},
		{
			name: "endpoint and protocol (http/protobuf)",
			config: otelConfig{
				endpoint: "http://otel-collector:4318",
				protocol: "http/protobuf",
			},
			expected: map[string]interface{}{
				"config": map[string]interface{}{
					"tracing": map[string]interface{}{
						"otlpEndpoint": "http://otel-collector:4318",
						"otlpProtocol": "http",
					},
				},
			},
		},
		{
			name: "endpoint with headers (comma-separated)",
			config: otelConfig{
				endpoint: "http://otel-collector:4318",
				headers:  "key1=value1,key2=value2",
			},
			expected: map[string]interface{}{
				"config": map[string]interface{}{
					"tracing": map[string]interface{}{
						"otlpEndpoint": "http://otel-collector:4318",
						"headers": map[string]interface{}{
							"key1": "value1",
							"key2": "value2",
						},
					},
				},
			},
		},
		{
			name: "signal-specific endpoint overrides general",
			config: otelConfig{
				endpoint:       "http://otel-collector:4318",
				tracesEndpoint: "http://traces-collector:4318",
			},
			expected: map[string]interface{}{
				"config": map[string]interface{}{
					"tracing": map[string]interface{}{
						"otlpEndpoint": "http://traces-collector:4318",
					},
				},
			},
		},
		{
			name: "signal-specific protocol overrides general",
			config: otelConfig{
				endpoint:       "http://otel-collector:4318",
				protocol:       "http/protobuf",
				tracesProtocol: "grpc",
			},
			expected: map[string]interface{}{
				"config": map[string]interface{}{
					"tracing": map[string]interface{}{
						"otlpEndpoint": "http://otel-collector:4318",
						"otlpProtocol": "grpc",
					},
				},
			},
		},
		{
			name: "signal-specific headers override general",
			config: otelConfig{
				endpoint:      "http://otel-collector:4318",
				headers:       "general=header",
				tracesHeaders: "trace-key=trace-value",
			},
			expected: map[string]interface{}{
				"config": map[string]interface{}{
					"tracing": map[string]interface{}{
						"otlpEndpoint": "http://otel-collector:4318",
						"headers": map[string]interface{}{
							"trace-key": "trace-value",
						},
					},
				},
			},
		},
		{
			name: "complete configuration with all fields",
			config: otelConfig{
				endpoint:       "http://otel-collector:4318",
				protocol:       "http/protobuf",
				tracesEndpoint: "http://traces-collector:4318",
				tracesProtocol: "grpc",
				tracesHeaders:  "auth=token123",
			},
			expected: map[string]interface{}{
				"config": map[string]interface{}{
					"tracing": map[string]interface{}{
						"otlpEndpoint": "http://traces-collector:4318",
						"otlpProtocol": "grpc",
						"headers": map[string]interface{}{
							"auth": "token123",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildTelemetryConfig(tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseHeaders(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: map[string]string{},
		},
		{
			name:  "single header",
			input: "key=value",
			expected: map[string]string{
				"key": "value",
			},
		},
		{
			name:  "multiple headers",
			input: "key1=value1,key2=value2",
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name:  "headers with spaces",
			input: " key1 = value1 , key2 = value2 ",
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name:  "header with value containing equals sign",
			input: "auth=Bearer token=abc123",
			expected: map[string]string{
				"auth": "Bearer token=abc123",
			},
		},
		{
			name:     "invalid header (no equals sign)",
			input:    "invalid",
			expected: map[string]string{},
		},
		{
			name:  "mixed valid and invalid headers",
			input: "valid=value,invalid,another=value2",
			expected: map[string]string{
				"valid":   "value",
				"another": "value2",
			},
		},
		{
			name:  "JSON format",
			input: `{"api-key":"secret123","environment":"production"}`,
			expected: map[string]string{
				"api-key":     "secret123",
				"environment": "production",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseHeaders(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
