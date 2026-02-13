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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentgatewayBackend represents an agentgateway backend resource
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type AgentgatewayBackend struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentgatewayBackendSpec   `json:"spec,omitempty"`
	Status AgentgatewayBackendStatus `json:"status,omitempty"`
}

// AgentgatewayBackendSpec defines the desired state of AgentgatewayBackend
type AgentgatewayBackendSpec struct {
	// MCP defines the MCP backend configuration
	// +optional
	MCP *MCPBackendSpec `json:"mcp,omitempty"`
}

// MCPBackendSpec defines the MCP backend configuration
type MCPBackendSpec struct {
	// Targets defines the list of MCP targets
	// +optional
	Targets []MCPTarget `json:"targets,omitempty"`
}

// MCPTarget defines a single MCP target
type MCPTarget struct {
	// Name is the name of the target
	// +optional
	Name string `json:"name,omitempty"`

	// Static defines static target configuration
	// +optional
	Static *StaticTarget `json:"static,omitempty"`
}

// StaticTarget defines a static target configuration
type StaticTarget struct {
	// Host is the target host
	// +optional
	Host string `json:"host,omitempty"`

	// Port is the target port
	// +optional
	Port int32 `json:"port,omitempty"`

	// Protocol is the protocol to use
	// +optional
	Protocol string `json:"protocol,omitempty"`
}

// AgentgatewayBackendStatus defines the observed state of AgentgatewayBackend
type AgentgatewayBackendStatus struct {
}

// AgentgatewayBackendList contains a list of AgentgatewayBackend
// +kubebuilder:object:root=true
type AgentgatewayBackendList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentgatewayBackend `json:"items"`
}
