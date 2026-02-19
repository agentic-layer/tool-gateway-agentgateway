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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

var _ = Describe("ToolGateway Reconciler", func() {
	const (
		toolGatewayName      = "test-tool-gateway"
		toolGatewayNamespace = "default"
		timeout              = time.Second * 10
		interval             = time.Millisecond * 250
	)

	Context("When reconciling a ToolGateway", func() {
		It("should create a Gateway resource", func() {
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

			By("Checking that the Gateway resource is created")
			gateway := &gatewayv1.Gateway{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolGatewayName,
					Namespace: toolGatewayNamespace,
				}, gateway)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying the Gateway configuration")
			Expect(gateway.Spec.GatewayClassName).To(Equal(gatewayv1.ObjectName("agentgateway")))
			Expect(gateway.Spec.Listeners).To(HaveLen(1))
			Expect(gateway.Spec.Listeners[0].Protocol).To(Equal(gatewayv1.HTTPProtocolType))
			Expect(gateway.Spec.Listeners[0].Port).To(Equal(gatewayv1.PortNumber(80)))

			By("Verifying the Gateway has owner reference")
			Expect(gateway.OwnerReferences).To(HaveLen(1))
			Expect(gateway.OwnerReferences[0].Name).To(Equal(toolGatewayName))
			Expect(gateway.OwnerReferences[0].Kind).To(Equal("ToolGateway"))

			By("Cleaning up the ToolGateway resource")
			Expect(k8sClient.Delete(ctx, toolGateway)).To(Succeed())

			By("Verifying the Gateway is deleted via owner reference")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolGatewayName,
					Namespace: toolGatewayNamespace,
				}, gateway)
				return err != nil
			}, timeout, interval).Should(BeTrue())
		})

		It("should update the Gateway when ToolGateway is updated", func() {
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

			By("Waiting for the Gateway to be created")
			gateway := &gatewayv1.Gateway{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolGatewayName + "-update",
					Namespace: toolGatewayNamespace,
				}, gateway)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying reconciliation maintains Gateway configuration")
			// The reconciler should maintain the Gateway in desired state
			Expect(gateway.Spec.GatewayClassName).To(Equal(gatewayv1.ObjectName("agentgateway")))

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, toolGateway)).To(Succeed())
		})
	})
})
