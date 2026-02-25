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
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

// newAgentgatewayBackend creates a new unstructured AgentgatewayBackend resource.
func newAgentgatewayBackend(name, namespace string) *unstructured.Unstructured {
	backend := &unstructured.Unstructured{}
	backend.SetAPIVersion("agentgateway.dev/v1alpha1")
	backend.SetKind("AgentgatewayBackend")
	backend.SetName(name)
	backend.SetNamespace(namespace)
	return backend
}

// buildMCPTarget builds a single MCP target entry for an AgentgatewayBackend spec.
func buildMCPTarget(name, host string, port int32) map[string]interface{} {
	return map[string]interface{}{
		"name": name,
		"static": map[string]interface{}{
			"host":     host,
			"port":     int64(port),
			"protocol": "StreamableHTTP",
		},
	}
}

// setMCPTargets sets the MCP targets in the AgentgatewayBackend spec.
func setMCPTargets(backend *unstructured.Unstructured, targets []interface{}) error {
	return unstructured.SetNestedMap(backend.Object, map[string]interface{}{
		"targets": targets,
	}, "spec", "mcp")
}

// buildHTTPRouteSpec builds an HTTPRouteSpec that routes traffic from the given gateway
// to an AgentgatewayBackend, matching the given path prefix.
func buildHTTPRouteSpec(gatewayName, gatewayNamespace, backendName, backendNamespace, path string) gatewayv1.HTTPRouteSpec {
	pathType := gatewayv1.PathMatchPathPrefix
	return gatewayv1.HTTPRouteSpec{
		CommonRouteSpec: gatewayv1.CommonRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{
				{
					Name:      gatewayv1.ObjectName(gatewayName),
					Namespace: ptr.To(gatewayv1.Namespace(gatewayNamespace)),
				},
			},
		},
		Rules: []gatewayv1.HTTPRouteRule{
			{
				BackendRefs: []gatewayv1.HTTPBackendRef{
					{
						BackendRef: gatewayv1.BackendRef{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group:     ptr.To(gatewayv1.Group("agentgateway.dev")),
								Kind:      ptr.To(gatewayv1.Kind("AgentgatewayBackend")),
								Name:      gatewayv1.ObjectName(backendName),
								Namespace: ptr.To(gatewayv1.Namespace(backendNamespace)),
							},
						},
					},
				},
				Matches: []gatewayv1.HTTPRouteMatch{
					{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  &pathType,
							Value: ptr.To(path),
						},
					},
				},
			},
		},
	}
}

// toolServerHost returns the cluster-local DNS name for a ToolServer.
func toolServerHost(name, namespace string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", name, namespace)
}

// newAgentgatewayParameters creates a new unstructured AgentgatewayParameters resource.
func newAgentgatewayParameters(name, namespace string) *unstructured.Unstructured {
	params := &unstructured.Unstructured{}
	params.SetAPIVersion("agentgateway.dev/v1alpha1")
	params.SetKind("AgentgatewayParameters")
	params.SetName(name)
	params.SetNamespace(namespace)
	return params
}

// setAgentgatewayParametersSpec writes spec.env and spec.deployment.spec (for envFrom)
// on the AgentgatewayParameters from the given ToolGatewaySpec.
//
// spec.env maps directly to AgentgatewayParameters.spec.env.
// spec.envFrom is injected via AgentgatewayParameters.spec.deployment.spec using
// strategic merge patch semantics — the "agentgateway" container entry is merged by name.
func setAgentgatewayParametersSpec(params *unstructured.Unstructured, toolGatewaySpec agentruntimev1alpha1.ToolGatewaySpec) error {
	// spec.env
	envVars, err := toUnstructuredSlice(toolGatewaySpec.Env)
	if err != nil {
		return fmt.Errorf("failed to convert spec.env: %w", err)
	}
	if err := unstructured.SetNestedSlice(params.Object, envVars, "spec", "env"); err != nil {
		return fmt.Errorf("failed to set spec.env: %w", err)
	}

	// spec.envFrom → spec.deployment.spec (strategic merge patch on the container)
	envFromSources, err := toUnstructuredSlice(toolGatewaySpec.EnvFrom)
	if err != nil {
		return fmt.Errorf("failed to convert spec.envFrom: %w", err)
	}
	deploymentSpec := map[string]interface{}{
		"template": map[string]interface{}{
			"spec": map[string]interface{}{
				"containers": []interface{}{
					map[string]interface{}{
						"name":    "agentgateway",
						"envFrom": envFromSources,
					},
				},
			},
		},
	}
	if err := unstructured.SetNestedMap(params.Object, deploymentSpec, "spec", "deployment", "spec"); err != nil {
		return fmt.Errorf("failed to set spec.deployment.spec: %w", err)
	}
	return nil
}

// toUnstructuredSlice converts a slice of any runtime.Object-compatible value to
// []interface{} suitable for use in an unstructured resource.
func toUnstructuredSlice[T any](items []T) ([]interface{}, error) {
	result := make([]interface{}, 0, len(items))
	for i := range items {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&items[i])
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, nil
}
