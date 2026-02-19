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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

var _ = Describe("ToolServer Reconciler", func() {
	const (
		toolServerName      = "test-tool-server"
		toolServerNamespace = "default"
	)

	var replicas int32 = 1

	Context("When reconciling a ToolServer", func() {
		It("should successfully handle ToolServer creation", func() {
			By("Creating a ToolServer resource")
			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      toolServerName,
					Namespace: toolServerNamespace,
				},
				Spec: agentruntimev1alpha1.ToolServerSpec{
					Protocol:      "mcp",
					TransportType: "http",
					Image:         "ghcr.io/agentic-layer/echo-mcp-server:0.1.0",
					Port:          8000,
					Path:          "/mcp",
					Replicas:      &replicas,
				},
			}
			Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())

			By("Cleaning up the ToolServer resource")
			Expect(k8sClient.Delete(ctx, toolServer)).To(Succeed())
		})
	})
})
