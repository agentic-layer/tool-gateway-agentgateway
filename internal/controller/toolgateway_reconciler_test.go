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

var _ = Describe("ToolGateway Reconciler", func() {
	const (
		toolGatewayName      = "test-tool-gateway"
		toolGatewayNamespace = "default"
	)

	Context("When reconciling a ToolGateway", func() {
		It("should successfully create the ToolGateway resource", func() {
			By("Creating a ToolGateway resource")
			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      toolGatewayName,
					Namespace: toolGatewayNamespace,
				},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "kgateway",
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

			By("Verifying the ToolGateway was created")
			createdToolGateway := &agentruntimev1alpha1.ToolGateway{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolGatewayName,
					Namespace: toolGatewayNamespace,
				}, createdToolGateway)
			}).Should(Succeed())

			Expect(createdToolGateway.Name).To(Equal(toolGatewayName))
			Expect(createdToolGateway.Spec.ToolGatewayClassName).To(Equal("kgateway"))

			By("Cleaning up the ToolGateway resource")
			Expect(k8sClient.Delete(ctx, toolGateway)).To(Succeed())
		})

		It("should handle updates to the ToolGateway", func() {
			By("Creating a ToolGateway resource")
			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      toolGatewayName + "-update",
					Namespace: toolGatewayNamespace,
				},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "kgateway",
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

			By("Updating the ToolGateway")
			Eventually(func() error {
				tg := &agentruntimev1alpha1.ToolGateway{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolGatewayName + "-update",
					Namespace: toolGatewayNamespace,
				}, tg); err != nil {
					return err
				}
				tg.Spec.ToolGatewayClassName = "updated-class"
				return k8sClient.Update(ctx, tg)
			}).Should(Succeed())

			By("Verifying the update")
			updatedToolGateway := &agentruntimev1alpha1.ToolGateway{}
			Eventually(func() string {
				_ = k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolGatewayName + "-update",
					Namespace: toolGatewayNamespace,
				}, updatedToolGateway)
				return updatedToolGateway.Spec.ToolGatewayClassName
			}).Should(Equal("updated-class"))

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, toolGateway)).To(Succeed())
		})
	})
})
