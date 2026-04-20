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
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

const (
	// metadataNamespace is the namespace used in AgentgatewayPolicy metadataContext
	metadataNamespace = "envoy.filters.http.ext_proc"
)

// GuardrailAdapter identifies the guardrail-adapter Kubernetes Service.
// An empty Namespace means the adapter is resolved from the ToolGateway's own namespace.
type GuardrailAdapter struct {
	Name      string
	Namespace string
	Port      int
}

// guardrailMetadata holds the structured metadata for a resolved guardrail
type guardrailMetadata struct {
	Provider       string
	Mode           string
	ProviderType   string
	ProviderConfig map[string]string
}

// resolveGuardrails resolves the Guard and GuardrailProvider resources referenced
// in the ToolGateway's guardrails field and returns the structured metadata.
func (r *ToolGatewayReconciler) resolveGuardrails(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) (*guardrailMetadata, error) {
	if len(toolGateway.Spec.Guardrails) == 0 {
		return nil, nil
	}

	// Currently only a single Guard is supported per ToolGateway
	// (agentgateway has only one ext_proc slot per target)
	if len(toolGateway.Spec.Guardrails) > 1 {
		return nil, fmt.Errorf("multiple guards not supported (agentgateway has only one ext_proc slot per target)")
	}

	guardRef := toolGateway.Spec.Guardrails[0]

	// Default namespace to ToolGateway's namespace if not specified
	guardNamespace := guardRef.Namespace
	if guardNamespace == "" {
		guardNamespace = toolGateway.Namespace
	}

	// Fetch the Guard resource
	var guard agentruntimev1alpha1.Guard
	guardKey := types.NamespacedName{
		Name:      guardRef.Name,
		Namespace: guardNamespace,
	}
	if err := r.Get(ctx, guardKey, &guard); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("guard %s/%s not found", guardNamespace, guardRef.Name)
		}
		return nil, fmt.Errorf("failed to get guard %s/%s: %w", guardNamespace, guardRef.Name, err)
	}

	// Resolve the GuardrailProvider
	providerNamespace := guard.Spec.ProviderRef.Namespace
	if providerNamespace == "" {
		providerNamespace = guard.Namespace
	}

	var provider agentruntimev1alpha1.GuardrailProvider
	providerKey := types.NamespacedName{
		Name:      guard.Spec.ProviderRef.Name,
		Namespace: providerNamespace,
	}
	if err := r.Get(ctx, providerKey, &provider); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("guardrail provider %s/%s not found", providerNamespace, guard.Spec.ProviderRef.Name)
		}
		return nil, fmt.Errorf("failed to get guardrail provider %s/%s: %w", providerNamespace, guard.Spec.ProviderRef.Name, err)
	}

	// Build metadata from Guard + GuardrailProvider
	return buildGuardrailMetadata(&guard, &provider)
}

// buildGuardrailMetadata builds a flat metadata map from Guard and GuardrailProvider config.
// The metadata is structured according to the guardrail-adapter's expected format.
func buildGuardrailMetadata(guard *agentruntimev1alpha1.Guard, provider *agentruntimev1alpha1.GuardrailProvider) (*guardrailMetadata, error) {
	metadata := &guardrailMetadata{
		Provider:       provider.Name,
		ProviderType:   provider.Spec.Type,
		ProviderConfig: make(map[string]string),
	}

	// Build mode string (comma-separated list of modes)
	modes := make([]string, len(guard.Spec.Mode))
	for i, mode := range guard.Spec.Mode {
		modes[i] = string(mode)
	}
	metadata.Mode = strings.Join(modes, ",")

	// Build provider-specific configuration
	switch provider.Spec.Type {
	case "presidio-api":
		if err := buildPresidioMetadata(guard, provider, metadata); err != nil {
			return nil, err
		}
	case "openai-moderation-api":
		if err := buildOpenAIModerationMetadata(guard, provider, metadata); err != nil {
			return nil, err
		}
	case "bedrock-api":
		if err := buildBedrockMetadata(guard, provider, metadata); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", provider.Spec.Type)
	}

	return metadata, nil
}

// buildPresidioMetadata builds metadata for Presidio provider
func buildPresidioMetadata(guard *agentruntimev1alpha1.Guard, provider *agentruntimev1alpha1.GuardrailProvider, metadata *guardrailMetadata) error {
	if provider.Spec.Presidio == nil {
		return fmt.Errorf("presidio provider config is missing")
	}

	// Provider endpoint
	metadata.ProviderConfig["presidio.endpoint"] = provider.Spec.Presidio.BaseUrl

	// Guard-level config (optional overrides)
	if guard.Spec.Presidio != nil {
		if guard.Spec.Presidio.Language != "" {
			metadata.ProviderConfig["presidio.language"] = guard.Spec.Presidio.Language
		}

		// Serialize ScoreThresholds map to JSON
		if len(guard.Spec.Presidio.ScoreThresholds) > 0 {
			thresholdsJSON, err := json.Marshal(guard.Spec.Presidio.ScoreThresholds)
			if err != nil {
				return fmt.Errorf("failed to marshal score thresholds: %w", err)
			}
			metadata.ProviderConfig["presidio.score_thresholds"] = string(thresholdsJSON)
		}

		// Serialize EntityActions map to JSON
		if len(guard.Spec.Presidio.EntityActions) > 0 {
			actionsJSON, err := json.Marshal(guard.Spec.Presidio.EntityActions)
			if err != nil {
				return fmt.Errorf("failed to marshal entity actions: %w", err)
			}
			metadata.ProviderConfig["presidio.entity_actions"] = string(actionsJSON)
		}
	}

	return nil
}

// buildOpenAIModerationMetadata builds metadata for OpenAI Moderation provider
func buildOpenAIModerationMetadata(guard *agentruntimev1alpha1.Guard, provider *agentruntimev1alpha1.GuardrailProvider, metadata *guardrailMetadata) error {
	if provider.Spec.OpenAIModeration == nil {
		return fmt.Errorf("openai-moderation provider config is missing")
	}

	// Provider endpoint (BaseUrl is optional, uses default OpenAI endpoint if not specified)
	if provider.Spec.OpenAIModeration.BaseUrl != "" {
		metadata.ProviderConfig["openai.endpoint"] = provider.Spec.OpenAIModeration.BaseUrl
	}

	// Guard-level config (optional overrides)
	if guard.Spec.OpenAIModeration != nil {
		if guard.Spec.OpenAIModeration.Model != "" {
			metadata.ProviderConfig["openai.model"] = guard.Spec.OpenAIModeration.Model
		}
	}

	return nil
}

// buildBedrockMetadata builds metadata for AWS Bedrock provider
func buildBedrockMetadata(guard *agentruntimev1alpha1.Guard, provider *agentruntimev1alpha1.GuardrailProvider, metadata *guardrailMetadata) error {
	if provider.Spec.Bedrock == nil {
		return fmt.Errorf("bedrock provider config is missing")
	}

	// Provider config
	metadata.ProviderConfig["bedrock.region"] = provider.Spec.Bedrock.Region

	// Guard-level config
	if guard.Spec.Bedrock != nil {
		metadata.ProviderConfig["bedrock.guardrail_id"] = guard.Spec.Bedrock.GuardrailId
		if guard.Spec.Bedrock.GuardrailVersion != "" {
			metadata.ProviderConfig["bedrock.guardrail_version"] = guard.Spec.Bedrock.GuardrailVersion
		}
	}

	return nil
}

// buildGuardrailPolicySpec builds the AgentgatewayPolicy spec map for a ToolGateway
// guarded by the given adapter and resolved metadata. The returned map uses only
// types that `unstructured.SetNestedMap` can DeepCopy (string, int64, nested maps,
// []interface{}).
func buildGuardrailPolicySpec(toolGatewayName string, adapter GuardrailAdapter, metadata *guardrailMetadata) map[string]interface{} {
	backendRef := map[string]interface{}{
		"name": adapter.Name,
		"port": int64(adapter.Port),
	}
	if adapter.Namespace != "" {
		backendRef["namespace"] = adapter.Namespace
	}

	return map[string]interface{}{
		"targetRefs": []interface{}{
			map[string]interface{}{
				"group": "gateway.networking.k8s.io",
				"kind":  "Gateway",
				"name":  toolGatewayName,
			},
		},
		"traffic": map[string]interface{}{
			"extProc": map[string]interface{}{
				"backendRef":  backendRef,
				"failureMode": "FailClosed",
				"metadataContext": map[string]interface{}{
					metadataNamespace: guardrailMetadataToCEL(metadata),
				},
			},
		},
	}
}

// guardrailMetadataToCEL converts guardrailMetadata to a CEL expression map for AgentgatewayPolicy.
// Each value is wrapped as a CEL string literal (e.g., "'value'"). The return type is
// map[string]interface{} so the result can be embedded directly in an unstructured
// AgentgatewayPolicy spec (DeepCopyJSONValue does not support map[string]string).
func guardrailMetadataToCEL(metadata *guardrailMetadata) map[string]interface{} {
	celMap := make(map[string]interface{})

	// Common fields
	celMap["guardrail.provider"] = fmt.Sprintf("'%s'", metadata.ProviderType)
	celMap["guardrail.mode"] = fmt.Sprintf("'%s'", metadata.Mode)

	// Provider-specific fields
	for key, value := range metadata.ProviderConfig {
		celKey := fmt.Sprintf("guardrail.%s", key)
		// Wrap value in CEL string literal
		celMap[celKey] = fmt.Sprintf("'%s'", value)
	}

	return celMap
}
