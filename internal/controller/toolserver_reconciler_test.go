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
	"k8s.io/apimachinery/pkg/types"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

var _ = Describe("ToolServer Reconciler", func() {
	const (
		toolServerName      = "test-tool-server"
		toolServerNamespace = "default"
	)

	var replicas int32 = 1

	Context("When reconciling a ToolServer", func() {
		It("should successfully create the ToolServer resource", func() {
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

			By("Verifying the ToolServer was created")
			createdToolServer := &agentruntimev1alpha1.ToolServer{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolServerName,
					Namespace: toolServerNamespace,
				}, createdToolServer)
			}).Should(Succeed())

			Expect(createdToolServer.Name).To(Equal(toolServerName))
			Expect(createdToolServer.Spec.Protocol).To(Equal("mcp"))
			Expect(createdToolServer.Spec.Port).To(Equal(int32(8000)))

			By("Cleaning up the ToolServer resource")
			Expect(k8sClient.Delete(ctx, toolServer)).To(Succeed())
		})

		It("should handle updates to the ToolServer", func() {
			By("Creating a ToolServer resource")
			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      toolServerName + "-update",
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

			By("Updating the ToolServer port")
			Eventually(func() error {
				ts := &agentruntimev1alpha1.ToolServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolServerName + "-update",
					Namespace: toolServerNamespace,
				}, ts); err != nil {
					return err
				}
				ts.Spec.Port = 9000
				return k8sClient.Update(ctx, ts)
			}).Should(Succeed())

			By("Verifying the update")
			updatedToolServer := &agentruntimev1alpha1.ToolServer{}
			Eventually(func() int32 {
				_ = k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolServerName + "-update",
					Namespace: toolServerNamespace,
				}, updatedToolServer)
				return updatedToolServer.Spec.Port
			}).Should(Equal(int32(9000)))

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, toolServer)).To(Succeed())
		})

		// Note: This test is commented out because it requires agent-runtime-operator v0.18.1 CRDs
		// which include the toolGatewayRef field. The test environment uses v0.18.0 CRDs.
		//
		// XIt("should handle ToolServer with toolGatewayRef", func() {
		// 	By("Creating a ToolServer with toolGatewayRef")
		// 	toolServer := &agentruntimev1alpha1.ToolServer{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name:      toolServerName + "-with-ref",
		// 			Namespace: toolServerNamespace,
		// 		},
		// 		Spec: agentruntimev1alpha1.ToolServerSpec{
		// 			Protocol:      "mcp",
		// 			TransportType: "http",
		// 			Image:         "ghcr.io/agentic-layer/echo-mcp-server:0.1.0",
		// 			Port:          8000,
		// 			Path:          "/mcp",
		// 			Replicas:      &replicas,
		// 			ToolGatewayRef: &corev1.ObjectReference{
		// 				Name:      "test-gateway",
		// 				Namespace: "default",
		// 			},
		// 		},
		// 	}
		// 	Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())
		//
		// 	By("Verifying the ToolServer with ref was created")
		// 	createdToolServer := &agentruntimev1alpha1.ToolServer{}
		// 	Eventually(func() error {
		// 		return k8sClient.Get(ctx, types.NamespacedName{
		// 			Name:      toolServerName + "-with-ref",
		// 			Namespace: toolServerNamespace,
		// 		}, createdToolServer)
		// 	}).Should(Succeed())
		//
		// 	// Note: The reconciler would process the ToolGatewayRef, but we're just
		// 	// testing that the resource can be created with the reference
		// 	Expect(createdToolServer.Spec.ToolGatewayRef).NotTo(BeNil())
		// 	Expect(createdToolServer.Spec.ToolGatewayRef.Name).To(Equal("test-gateway"))
		//
		// 	By("Cleaning up")
		// 	Expect(k8sClient.Delete(ctx, toolServer)).To(Succeed())
		// })
	})
})
