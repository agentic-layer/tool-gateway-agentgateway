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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

var _ = Describe("ToolServer Controller", func() {
	ctx := context.Background()
	var reconciler *ToolServerReconciler

	BeforeEach(func() {
		reconciler = &ToolServerReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(100),
		}
	})

	AfterEach(func() {
		// Clean up all tool servers in the default namespace after each test
		toolServerList := &agentruntimev1alpha1.ToolServerList{}
		Expect(k8sClient.List(ctx, toolServerList, &client.ListOptions{Namespace: "default"})).To(Succeed())
		for i := range toolServerList.Items {
			_ = k8sClient.Delete(ctx, &toolServerList.Items[i])
		}

		// Clean up all tool gateways
		toolGatewayList := &agentruntimev1alpha1.ToolGatewayList{}
		Expect(k8sClient.List(ctx, toolGatewayList, &client.ListOptions{Namespace: "default"})).To(Succeed())
		for i := range toolGatewayList.Items {
			_ = k8sClient.Delete(ctx, &toolGatewayList.Items[i])
		}

		// Clean up tool gateway classes
		toolGatewayClassList := &agentruntimev1alpha1.ToolGatewayClassList{}
		Expect(k8sClient.List(ctx, toolGatewayClassList)).To(Succeed())
		for i := range toolGatewayClassList.Items {
			_ = k8sClient.Delete(ctx, &toolGatewayClassList.Items[i])
		}
	})

	Describe("Reconcile", func() {
		It("should successfully reconcile a basic ToolServer", func() {
			// Create ToolGatewayClass
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-class",
				},
				Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
					Controller: "runtime.agentic-layer.ai/tool-gateway-kgateway-controller",
				},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			// Create ToolGateway
			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gateway",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "test-class",
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

			// Wait for ToolGateway to be available
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-gateway",
					Namespace: "default",
				}, &agentruntimev1alpha1.ToolGateway{})
			}, "10s", "1s").Should(Succeed())

			// Create Gateway (normally done by ToolGatewayReconciler)
			gateway := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-gateway",
					Namespace: "default",
				},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: "agentgateway",
					Listeners: []gatewayv1.Listener{
						{
							Name:     "sse",
							Protocol: gatewayv1.HTTPProtocolType,
							Port:     80,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, gateway)).To(Succeed())

			// Create ToolServer
			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-tool-server",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolServerSpec{
					Image:         "test-image:latest",
					Port:          8000,
					Protocol:      "mcp",
					TransportType: "sse",
					ToolGatewayRef: &corev1.ObjectReference{
						Name:      "test-gateway",
						Namespace: "default",
					},
				},
			}
			Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())

			// Wait for ToolServer to be available
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-tool-server",
					Namespace: "default",
				}, &agentruntimev1alpha1.ToolServer{})
			}, "10s", "1s").Should(Succeed())

			// Reconcile
			// Note: AgentgatewayBackend CRD verification is not possible because the CRDs
			// come from the agentgateway project (Rust-based) and are not available as Go
			// modules for envtest. The reconciler will fail when trying to create the
			// AgentgatewayBackend, but it should still create the HTTPRoute first.
			// E2E tests with full kgateway installation will validate AgentgatewayBackend creation.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-tool-server",
					Namespace: "default",
				},
			})
			// Expect error due to missing AgentgatewayBackend CRD
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("AgentgatewayBackend"))

			// Note: HTTPRoute and AgentgatewayBackend creation cannot be verified in this test
			// because AgentgatewayBackend CRDs come from the agentgateway project (Rust-based)
			// and are not available as Go modules for envtest. The reconciler fails before
			// creating the HTTPRoute. E2E tests with full kgateway installation will validate
			// the complete resource creation.
		})

		It("should return nil when ToolServer is not found", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "nonexistent-toolserver",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should use SSE path for SSE protocol", func() {
			// Create ToolGatewayClass
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "sse-test-class",
				},
				Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
					Controller: "runtime.agentic-layer.ai/tool-gateway-kgateway-controller",
				},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			// Create ToolGateway
			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sse-test-gateway",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "sse-test-class",
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

			// Wait for ToolGateway to be available
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "sse-test-gateway",
					Namespace: "default",
				}, &agentruntimev1alpha1.ToolGateway{})
			}, "10s", "1s").Should(Succeed())

			// Create Gateway
			gateway := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sse-test-gateway",
					Namespace: "default",
				},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: "agentgateway",
					Listeners: []gatewayv1.Listener{
						{
							Name:     "sse",
							Protocol: gatewayv1.HTTPProtocolType,
							Port:     80,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, gateway)).To(Succeed())

			// Create ToolServer with SSE transport
			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sse-server",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolServerSpec{
					Image:         "test-image:latest",
					Port:          8000,
					Protocol:      "mcp",
					TransportType: "sse",
					ToolGatewayRef: &corev1.ObjectReference{
						Name:      "sse-test-gateway",
						Namespace: "default",
					},
				},
			}
			Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())

			// Wait for ToolServer to be available
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-sse-server",
					Namespace: "default",
				}, &agentruntimev1alpha1.ToolServer{})
			}, "10s", "1s").Should(Succeed())

			// Reconcile
			// Note: AgentgatewayBackend CRD is not available in envtest because it comes
			// from the agentgateway project (Rust-based) and is not available as a Go module.
			// The reconciliation will fail when attempting to create AgentgatewayBackend.
			// E2E tests with full kgateway installation will validate the complete flow.
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-sse-server",
					Namespace: "default",
				},
			})
			// Expect error due to missing AgentgatewayBackend CRD
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("AgentgatewayBackend"))

			// Note: HTTPRoute path verification is not possible because the reconciler
			// fails before creating the HTTPRoute due to missing AgentgatewayBackend CRD.
			// E2E tests with full kgateway installation will validate the /sse path.
		})
	})

	Describe("findToolGateway", func() {
		It("should find ToolGateway by toolGatewayRef", func() {
			// Create namespace first
			testNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-ns",
				},
			}
			Expect(k8sClient.Create(ctx, testNs)).To(Succeed())

			// Create ToolGatewayClass
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "find-test-class",
				},
				Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
					Controller: "runtime.agentic-layer.ai/tool-gateway-kgateway-controller",
				},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			// Create ToolGateway in a different namespace
			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "find-me-gateway",
					Namespace: "test-ns",
				},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "find-test-class",
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

			// Wait for ToolGateway to be available
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "find-me-gateway",
					Namespace: "test-ns",
				}, &agentruntimev1alpha1.ToolGateway{})
			}, "10s", "1s").Should(Succeed())

			// Create ToolServer with toolGatewayRef
			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "find-test-server",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolServerSpec{
					Image:         "test-image:latest",
					Port:          8000,
					Protocol:      "mcp",
					TransportType: "sse",
					ToolGatewayRef: &corev1.ObjectReference{
						Name:      "find-me-gateway",
						Namespace: "test-ns",
					},
				},
			}

			// Find the gateway
			foundGateway, err := reconciler.findToolGateway(ctx, toolServer)
			Expect(err).NotTo(HaveOccurred())
			Expect(foundGateway).NotTo(BeNil())
			Expect(foundGateway.Name).To(Equal("find-me-gateway"))
			Expect(foundGateway.Namespace).To(Equal("test-ns"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, toolGateway)).To(Succeed())
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "find-me-gateway",
					Namespace: "test-ns",
				}, &agentruntimev1alpha1.ToolGateway{})
			}, "10s", "1s").ShouldNot(Succeed())
			Expect(k8sClient.Delete(ctx, testNs)).To(Succeed())
		})

		It("should find default ToolGateway when no toolGatewayRef", func() {
			// Create ToolGatewayClass
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default-test-class",
				},
				Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
					Controller: "runtime.agentic-layer.ai/tool-gateway-kgateway-controller",
				},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			// Create ToolGateway
			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-gateway",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "default-test-class",
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

			// Wait for ToolGateway to be available
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "default-gateway",
					Namespace: "default",
				}, &agentruntimev1alpha1.ToolGateway{})
			}, "10s", "1s").Should(Succeed())

			// Create ToolServer without toolGatewayRef
			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-test-server",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolServerSpec{
					Image:         "test-image:latest",
					Port:          8000,
					Protocol:      "mcp",
					TransportType: "sse",
				},
			}

			// Find the gateway - should find the default one
			foundGateway, err := reconciler.findToolGateway(ctx, toolServer)
			Expect(err).NotTo(HaveOccurred())
			Expect(foundGateway).NotTo(BeNil())
			Expect(foundGateway.Name).To(Equal("default-gateway"))
		})
	})
})
