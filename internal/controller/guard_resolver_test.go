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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

func TestBuildPresidioMetadata(t *testing.T) {
	tests := []struct {
		name           string
		guard          *agentruntimev1alpha1.Guard
		provider       *agentruntimev1alpha1.GuardrailProvider
		expectedConfig map[string]string
		expectError    bool
	}{
		{
			name: "provider config only",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					Presidio: &agentruntimev1alpha1.PresidioProviderConfig{
						BaseUrl: "http://presidio-analyzer:8080",
					},
				},
			},
			expectedConfig: map[string]string{
				"presidio.endpoint": "http://presidio-analyzer:8080",
			},
			expectError: false,
		},
		{
			name: "guard with language override",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{
					Presidio: &agentruntimev1alpha1.PresidioGuardConfig{
						Language: "en",
					},
				},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					Presidio: &agentruntimev1alpha1.PresidioProviderConfig{
						BaseUrl: "http://presidio-analyzer:8080",
					},
				},
			},
			expectedConfig: map[string]string{
				"presidio.endpoint": "http://presidio-analyzer:8080",
				"presidio.language": "en",
			},
			expectError: false,
		},
		{
			name: "guard with score thresholds",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{
					Presidio: &agentruntimev1alpha1.PresidioGuardConfig{
						ScoreThresholds: map[string]string{
							"PERSON":        "0.8",
							"EMAIL_ADDRESS": "0.7",
						},
					},
				},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					Presidio: &agentruntimev1alpha1.PresidioProviderConfig{
						BaseUrl: "http://presidio-analyzer:8080",
					},
				},
			},
			expectedConfig: map[string]string{
				"presidio.endpoint": "http://presidio-analyzer:8080",
				// score_thresholds is JSON-encoded
				"presidio.score_thresholds": `{"EMAIL_ADDRESS":"0.7","PERSON":"0.8"}`,
			},
			expectError: false,
		},
		{
			name: "guard with entity actions",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{
					Presidio: &agentruntimev1alpha1.PresidioGuardConfig{
						EntityActions: map[string]string{
							"PERSON":      "MASK",
							"CREDIT_CARD": "BLOCK",
						},
					},
				},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					Presidio: &agentruntimev1alpha1.PresidioProviderConfig{
						BaseUrl: "http://presidio-analyzer:8080",
					},
				},
			},
			expectedConfig: map[string]string{
				"presidio.endpoint": "http://presidio-analyzer:8080",
				// entity_actions is JSON-encoded
				"presidio.entity_actions": `{"CREDIT_CARD":"BLOCK","PERSON":"MASK"}`,
			},
			expectError: false,
		},
		{
			name:  "missing provider config",
			guard: &agentruntimev1alpha1.Guard{},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := &guardrailMetadata{
				ProviderConfig: make(map[string]string),
			}
			err := buildPresidioMetadata(tt.guard, tt.provider, metadata)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedConfig, metadata.ProviderConfig)
			}
		})
	}
}

func TestBuildOpenAIModerationMetadata(t *testing.T) {
	tests := []struct {
		name           string
		guard          *agentruntimev1alpha1.Guard
		provider       *agentruntimev1alpha1.GuardrailProvider
		expectedConfig map[string]string
		expectError    bool
	}{
		{
			name: "provider with default endpoint",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					OpenAIModeration: &agentruntimev1alpha1.OpenAIModerationProviderConfig{},
				},
			},
			expectedConfig: map[string]string{},
			expectError:    false,
		},
		{
			name: "provider with custom endpoint",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					OpenAIModeration: &agentruntimev1alpha1.OpenAIModerationProviderConfig{
						BaseUrl: "https://custom-openai-endpoint.com",
					},
				},
			},
			expectedConfig: map[string]string{
				"openai.endpoint": "https://custom-openai-endpoint.com",
			},
			expectError: false,
		},
		{
			name: "guard with model override",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{
					OpenAIModeration: &agentruntimev1alpha1.OpenAIModerationGuardConfig{
						Model: "text-moderation-stable",
					},
				},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					OpenAIModeration: &agentruntimev1alpha1.OpenAIModerationProviderConfig{},
				},
			},
			expectedConfig: map[string]string{
				"openai.model": "text-moderation-stable",
			},
			expectError: false,
		},
		{
			name:  "missing provider config",
			guard: &agentruntimev1alpha1.Guard{},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := &guardrailMetadata{
				ProviderConfig: make(map[string]string),
			}
			err := buildOpenAIModerationMetadata(tt.guard, tt.provider, metadata)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedConfig, metadata.ProviderConfig)
			}
		})
	}
}

func TestBuildBedrockMetadata(t *testing.T) {
	tests := []struct {
		name           string
		guard          *agentruntimev1alpha1.Guard
		provider       *agentruntimev1alpha1.GuardrailProvider
		expectedConfig map[string]string
		expectError    bool
	}{
		{
			name: "complete bedrock config",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{
					Bedrock: &agentruntimev1alpha1.BedrockGuardConfig{
						GuardrailId: "12345678",
					},
				},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					Bedrock: &agentruntimev1alpha1.BedrockProviderConfig{
						Region: "us-west-2",
					},
				},
			},
			expectedConfig: map[string]string{
				"bedrock.region":       "us-west-2",
				"bedrock.guardrail_id": "12345678",
			},
			expectError: false,
		},
		{
			name: "bedrock with version",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{
					Bedrock: &agentruntimev1alpha1.BedrockGuardConfig{
						GuardrailId:      "12345678",
						GuardrailVersion: "DRAFT",
					},
				},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					Bedrock: &agentruntimev1alpha1.BedrockProviderConfig{
						Region: "us-east-1",
					},
				},
			},
			expectedConfig: map[string]string{
				"bedrock.region":            "us-east-1",
				"bedrock.guardrail_id":      "12345678",
				"bedrock.guardrail_version": "DRAFT",
			},
			expectError: false,
		},
		{
			name:  "missing provider config",
			guard: &agentruntimev1alpha1.Guard{},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := &guardrailMetadata{
				ProviderConfig: make(map[string]string),
			}
			err := buildBedrockMetadata(tt.guard, tt.provider, metadata)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedConfig, metadata.ProviderConfig)
			}
		})
	}
}

func TestBuildGuardrailMetadata(t *testing.T) {
	tests := []struct {
		name             string
		guard            *agentruntimev1alpha1.Guard
		provider         *agentruntimev1alpha1.GuardrailProvider
		expectedProvider string
		expectedType     string
		expectedMode     string
		expectError      bool
	}{
		{
			name: "presidio with single mode",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{
					Mode: []agentruntimev1alpha1.GuardMode{
						agentruntimev1alpha1.GuardModePreCall,
					},
				},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				ObjectMeta: metav1.ObjectMeta{
					Name: "presidio-analyzer",
				},
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					Type: "presidio-api",
					Presidio: &agentruntimev1alpha1.PresidioProviderConfig{
						BaseUrl: "http://presidio:8080",
					},
				},
			},
			expectedProvider: "presidio-analyzer",
			expectedType:     "presidio-api",
			expectedMode:     "pre_call",
			expectError:      false,
		},
		{
			name: "presidio with multiple modes",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{
					Mode: []agentruntimev1alpha1.GuardMode{
						agentruntimev1alpha1.GuardModePreCall,
						agentruntimev1alpha1.GuardModePostCall,
					},
				},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				ObjectMeta: metav1.ObjectMeta{
					Name: "presidio-analyzer",
				},
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					Type: "presidio-api",
					Presidio: &agentruntimev1alpha1.PresidioProviderConfig{
						BaseUrl: "http://presidio:8080",
					},
				},
			},
			expectedProvider: "presidio-analyzer",
			expectedType:     "presidio-api",
			expectedMode:     "pre_call,post_call",
			expectError:      false,
		},
		{
			name: "openai-moderation",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{
					Mode: []agentruntimev1alpha1.GuardMode{
						agentruntimev1alpha1.GuardModePreCall,
					},
				},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				ObjectMeta: metav1.ObjectMeta{
					Name: "openai-moderator",
				},
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					Type:             "openai-moderation-api",
					OpenAIModeration: &agentruntimev1alpha1.OpenAIModerationProviderConfig{},
				},
			},
			expectedProvider: "openai-moderator",
			expectedType:     "openai-moderation-api",
			expectedMode:     "pre_call",
			expectError:      false,
		},
		{
			name: "bedrock",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{
					Mode: []agentruntimev1alpha1.GuardMode{
						agentruntimev1alpha1.GuardModePreCall,
						agentruntimev1alpha1.GuardModePostCall,
					},
					Bedrock: &agentruntimev1alpha1.BedrockGuardConfig{
						GuardrailId: "12345678",
					},
				},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				ObjectMeta: metav1.ObjectMeta{
					Name: "bedrock-guardrail",
				},
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					Type: "bedrock-api",
					Bedrock: &agentruntimev1alpha1.BedrockProviderConfig{
						Region: "us-west-2",
					},
				},
			},
			expectedProvider: "bedrock-guardrail",
			expectedType:     "bedrock-api",
			expectedMode:     "pre_call,post_call",
			expectError:      false,
		},
		{
			name: "unsupported provider type",
			guard: &agentruntimev1alpha1.Guard{
				Spec: agentruntimev1alpha1.GuardSpec{
					Mode: []agentruntimev1alpha1.GuardMode{
						agentruntimev1alpha1.GuardModePreCall,
					},
				},
			},
			provider: &agentruntimev1alpha1.GuardrailProvider{
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					Type: "unsupported-type",
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata, err := buildGuardrailMetadata(tt.guard, tt.provider)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedProvider, metadata.Provider)
				assert.Equal(t, tt.expectedType, metadata.ProviderType)
				assert.Equal(t, tt.expectedMode, metadata.Mode)
				assert.NotNil(t, metadata.ProviderConfig)
			}
		})
	}
}

func TestGuardrailMetadataToCEL(t *testing.T) {
	tests := []struct {
		name     string
		metadata *guardrailMetadata
		expected map[string]interface{}
	}{
		{
			name: "presidio metadata",
			metadata: &guardrailMetadata{
				ProviderType: "presidio-api",
				Mode:         "pre_call,post_call",
				ProviderConfig: map[string]string{
					"presidio.endpoint": "http://presidio:8080",
					"presidio.language": "en",
				},
			},
			expected: map[string]interface{}{
				"guardrail.provider":          "'presidio-api'",
				"guardrail.mode":              "'pre_call,post_call'",
				"guardrail.presidio.endpoint": "'http://presidio:8080'",
				"guardrail.presidio.language": "'en'",
			},
		},
		{
			name: "openai metadata",
			metadata: &guardrailMetadata{
				ProviderType: "openai-moderation-api",
				Mode:         "pre_call",
				ProviderConfig: map[string]string{
					"openai.model": "text-moderation-latest",
				},
			},
			expected: map[string]interface{}{
				"guardrail.provider":     "'openai-moderation-api'",
				"guardrail.mode":         "'pre_call'",
				"guardrail.openai.model": "'text-moderation-latest'",
			},
		},
		{
			name: "bedrock metadata",
			metadata: &guardrailMetadata{
				ProviderType: "bedrock-api",
				Mode:         "pre_call,post_call",
				ProviderConfig: map[string]string{
					"bedrock.region":       "us-west-2",
					"bedrock.guardrail_id": "12345678",
				},
			},
			expected: map[string]interface{}{
				"guardrail.provider":             "'bedrock-api'",
				"guardrail.mode":                 "'pre_call,post_call'",
				"guardrail.bedrock.region":       "'us-west-2'",
				"guardrail.bedrock.guardrail_id": "'12345678'",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := guardrailMetadataToCEL(tt.metadata)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildGuardrailPolicySpec(t *testing.T) {
	metadata := &guardrailMetadata{
		ProviderType:   "presidio-api",
		Mode:           "pre_call",
		ProviderConfig: map[string]string{"presidio.endpoint": "http://presidio:80"},
	}

	tests := []struct {
		name              string
		adapter           GuardrailAdapter
		expectedName      string
		expectedPort      int64
		expectedNamespace string
		namespaceSet      bool
	}{
		{
			name:         "default adapter without namespace",
			adapter:      GuardrailAdapter{Name: "guardrail-adapter", Port: 80},
			expectedName: "guardrail-adapter",
			expectedPort: 80,
			namespaceSet: false,
		},
		{
			name:              "adapter with explicit namespace",
			adapter:           GuardrailAdapter{Name: "guardrail-adapter", Namespace: "guardrails", Port: 9001},
			expectedName:      "guardrail-adapter",
			expectedPort:      9001,
			expectedNamespace: "guardrails",
			namespaceSet:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := buildGuardrailPolicySpec("my-gateway", tt.adapter, metadata)

			// Regression guard: feeding the spec into unstructured.SetNestedMap
			// panics if any value uses a Go type DeepCopyJSONValue can't handle
			// (for example, plain int instead of int64).
			obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
			require.NotPanics(t, func() {
				err := unstructured.SetNestedMap(obj.Object, spec, "spec")
				require.NoError(t, err)
			}, "policy spec must only contain DeepCopyJSONValue-compatible types")

			traffic, ok := spec["traffic"].(map[string]interface{})
			require.True(t, ok, "spec.traffic must be a map")
			extProc, ok := traffic["extProc"].(map[string]interface{})
			require.True(t, ok, "spec.traffic.extProc must be a map")
			backendRef, ok := extProc["backendRef"].(map[string]interface{})
			require.True(t, ok, "backendRef must be a map")

			assert.Equal(t, tt.expectedName, backendRef["name"])
			port, ok := backendRef["port"].(int64)
			require.True(t, ok, "backendRef.port must be int64 (not int) to survive unstructured deep-copy")
			assert.Equal(t, tt.expectedPort, port)

			if tt.namespaceSet {
				assert.Equal(t, tt.expectedNamespace, backendRef["namespace"])
			} else {
				_, hasNamespace := backendRef["namespace"]
				assert.False(t, hasNamespace, "namespace key must be omitted when adapter namespace is empty")
			}

			assert.Equal(t, "FailClosed", extProc["failureMode"])

			targetRefs, ok := spec["targetRefs"].([]interface{})
			require.True(t, ok, "spec.targetRefs must be a slice")
			require.Len(t, targetRefs, 1)
			targetRef, ok := targetRefs[0].(map[string]interface{})
			require.True(t, ok)
			assert.Equal(t, "my-gateway", targetRef["name"])
			assert.Equal(t, "Gateway", targetRef["kind"])
			assert.Equal(t, "gateway.networking.k8s.io", targetRef["group"])
		})
	}
}
