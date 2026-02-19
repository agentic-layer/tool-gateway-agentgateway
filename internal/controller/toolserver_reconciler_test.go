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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

var _ = Describe("ToolServer Reconciler", func() {
	const (
		toolServerName       = "test-tool-server"
		toolServerNamespace  = "default"
		toolGatewayName      = "test-gateway-for-server"
		toolGatewayClassName = "kgateway"
		timeout              = time.Second * 10
		interval             = time.Millisecond * 250
	)

	var replicas int32 = 1

	BeforeEach(func() {
		By("Creating a ToolGatewayClass")
		toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: toolGatewayClassName,
			},
			Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
				Controller: "runtime.agentic-layer.ai/tool-gateway-kgateway-controller",
			},
		}
		Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

		By("Creating a ToolGateway for the ToolServer to reference")
		toolGateway := &agentruntimev1alpha1.ToolGateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      toolGatewayName,
				Namespace: toolServerNamespace,
			},
			Spec: agentruntimev1alpha1.ToolGatewaySpec{
				ToolGatewayClassName: toolGatewayClassName,
			},
		}
		Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

		By("Waiting for the Gateway to be created")
		gateway := &gatewayv1.Gateway{}
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      toolGatewayName,
				Namespace: toolServerNamespace,
			}, gateway)
			return err == nil
		}, timeout, interval).Should(BeTrue())
	})

	AfterEach(func() {
		By("Cleaning up the ToolGateway")
		toolGateway := &agentruntimev1alpha1.ToolGateway{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      toolGatewayName,
			Namespace: toolServerNamespace,
		}, toolGateway)
		if err == nil {
			Expect(k8sClient.Delete(ctx, toolGateway)).To(Succeed())
		}

		By("Cleaning up the ToolGatewayClass")
		toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: toolGatewayClassName}, toolGatewayClass)
		if err == nil {
			Expect(k8sClient.Delete(ctx, toolGatewayClass)).To(Succeed())
		}
	})

	Context("When reconciling a ToolServer", func() {
		It("should create AgentgatewayBackend and HTTPRoute resources", func() {
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

			By("Checking that the AgentgatewayBackend resource is created")
			backend := &unstructured.Unstructured{}
			backend.SetAPIVersion("agentgateway.dev/v1alpha1")
			backend.SetKind("AgentgatewayBackend")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolServerName,
					Namespace: toolServerNamespace,
				}, backend)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying the AgentgatewayBackend configuration")
			spec, found, err := unstructured.NestedMap(backend.Object, "spec", "mcp")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(spec).NotTo(BeNil())

			By("Verifying the AgentgatewayBackend has owner reference")
			Expect(backend.GetOwnerReferences()).To(HaveLen(1))
			Expect(backend.GetOwnerReferences()[0].Name).To(Equal(toolServerName))
			Expect(backend.GetOwnerReferences()[0].Kind).To(Equal("ToolServer"))

			By("Checking that the HTTPRoute resource is created")
			httpRoute := &gatewayv1.HTTPRoute{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolServerName,
					Namespace: toolServerNamespace,
				}, httpRoute)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying the HTTPRoute configuration")
			Expect(httpRoute.Spec.ParentRefs).To(HaveLen(1))
			Expect(string(httpRoute.Spec.ParentRefs[0].Name)).To(Equal(toolGatewayName))
			Expect(httpRoute.Spec.Rules).To(HaveLen(1))
			Expect(httpRoute.Spec.Rules[0].BackendRefs).To(HaveLen(1))
			Expect(string(httpRoute.Spec.Rules[0].BackendRefs[0].Name)).To(Equal(toolServerName))

			By("Verifying the HTTPRoute has owner reference")
			Expect(httpRoute.OwnerReferences).To(HaveLen(1))
			Expect(httpRoute.OwnerReferences[0].Name).To(Equal(toolServerName))
			Expect(httpRoute.OwnerReferences[0].Kind).To(Equal("ToolServer"))

			By("Cleaning up the ToolServer resource")
			Expect(k8sClient.Delete(ctx, toolServer)).To(Succeed())

			By("Verifying the HTTPRoute is deleted via owner reference")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolServerName,
					Namespace: toolServerNamespace,
				}, httpRoute)
				return err != nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying the AgentgatewayBackend is deleted via owner reference")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolServerName,
					Namespace: toolServerNamespace,
				}, backend)
				return err != nil
			}, timeout, interval).Should(BeTrue())
		})

		It("should handle ToolServer with toolGatewayRef", func() {
			By("Creating a ToolServer with toolGatewayRef")
			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      toolServerName + "-with-ref",
					Namespace: toolServerNamespace,
				},
				Spec: agentruntimev1alpha1.ToolServerSpec{
					Protocol:      "mcp",
					TransportType: "http",
					Image:         "ghcr.io/agentic-layer/echo-mcp-server:0.1.0",
					Port:          8000,
					Path:          "/mcp",
					Replicas:      &replicas,
					ToolGatewayRef: &corev1.ObjectReference{
						Name: toolGatewayName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())

			By("Waiting for HTTPRoute to be created")
			httpRoute := &gatewayv1.HTTPRoute{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      toolServerName + "-with-ref",
					Namespace: toolServerNamespace,
				}, httpRoute)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			By("Verifying HTTPRoute references the correct Gateway")
			Expect(httpRoute.Spec.ParentRefs).To(HaveLen(1))
			Expect(string(httpRoute.Spec.ParentRefs[0].Name)).To(Equal(toolGatewayName))

			By("Cleaning up")
			Expect(k8sClient.Delete(ctx, toolServer)).To(Succeed())
		})
	})
})
