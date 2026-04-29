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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"sigs.k8s.io/yaml"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

const (
	providerTypePresidio = "presidio-api"

	// staticConfigKey is the key inside the per-Guard ConfigMap that the
	// adapter reads via GUARDRAIL_CONFIG_FILE.
	staticConfigKey = "config.yaml"
)

var (
	errUnsupportedProvider = errors.New("unsupported provider type")
	errInvalidConfig       = errors.New("invalid guard config")
)

// staticConfig mirrors guardrail-adapter's on-disk schema (internal/metadata).
// json tags are intentional: sigs.k8s.io/yaml goes via JSON, so map values are
// emitted with sorted keys, giving us deterministic output for free.
type staticConfig struct {
	Provider string          `json:"provider"`
	Modes    []string        `json:"modes"`
	Presidio *staticPresidio `json:"presidio,omitempty"`
}

type staticPresidio struct {
	Endpoint        string            `json:"endpoint"`
	Language        string            `json:"language,omitempty"`
	ScoreThresholds map[string]string `json:"score_thresholds,omitempty"`
	EntityActions   map[string]string `json:"entity_actions,omitempty"`
}

// renderStaticConfig builds the YAML body for the per-Guard adapter ConfigMap
// and a sha256 hex hash of those bytes. The hash is used as a pod-template
// annotation so the Deployment rolls when the ConfigMap changes.
func renderStaticConfig(guard *agentruntimev1alpha1.Guard, provider *agentruntimev1alpha1.GuardrailProvider) ([]byte, string, error) {
	if provider.Spec.Type != providerTypePresidio {
		return nil, "", fmt.Errorf("%w: %q", errUnsupportedProvider, provider.Spec.Type)
	}

	if len(guard.Spec.Mode) == 0 {
		return nil, "", fmt.Errorf("%w: modes is required", errInvalidConfig)
	}
	for _, m := range guard.Spec.Mode {
		switch m {
		case agentruntimev1alpha1.GuardModePreCall, agentruntimev1alpha1.GuardModePostCall:
		default:
			return nil, "", fmt.Errorf("%w: mode %q not supported by adapter (allowed: pre_call, post_call)", errInvalidConfig, m)
		}
	}

	cfg := &staticConfig{
		Provider: providerTypePresidio,
		Modes:    make([]string, 0, len(guard.Spec.Mode)),
	}
	for _, m := range guard.Spec.Mode {
		cfg.Modes = append(cfg.Modes, string(m))
	}

	if provider.Spec.Presidio == nil || provider.Spec.Presidio.BaseUrl == "" {
		return nil, "", fmt.Errorf("%w: presidio.baseUrl is required", errInvalidConfig)
	}
	cfg.Presidio = &staticPresidio{Endpoint: provider.Spec.Presidio.BaseUrl}
	if guard.Spec.Presidio != nil {
		cfg.Presidio.Language = guard.Spec.Presidio.Language
		cfg.Presidio.ScoreThresholds = guard.Spec.Presidio.ScoreThresholds
		cfg.Presidio.EntityActions = guard.Spec.Presidio.EntityActions
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("marshal static config: %w", err)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}
