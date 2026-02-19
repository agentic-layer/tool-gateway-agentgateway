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

	// Helper function to create test setup
	setupTestGatewayAndClass := func(className, gatewayName string) {
		toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: className,
			},
			Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
				Controller: "runtime.agentic-layer.ai/tool-gateway-kgateway-controller",
			},
		}
		Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

		toolGateway := &agentruntimev1alpha1.ToolGateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      gatewayName,
				Namespace: "default",
			},
			Spec: agentruntimev1alpha1.ToolGatewaySpec{
				ToolGatewayClassName: className,
			},
		}
		Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      gatewayName,
				Namespace: "default",
			}, &agentruntimev1alpha1.ToolGateway{})
		}, "10s", "1s").Should(Succeed())
	}

	Describe("Reconcile", func() {
		It("should create HTTPRoute for HTTP transport", func() {
			setupTestGatewayAndClass("test-class", "test-gateway")

			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-tool-server",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolServerSpec{
					Image:         "test-image:latest",
					Port:          8000,
					Protocol:      "mcp",
					TransportType: "http",
					ToolGatewayRef: &corev1.ObjectReference{
						Name:      "test-gateway",
						Namespace: "default",
					},
				},
			}
			Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-tool-server",
					Namespace: "default",
				}, &agentruntimev1alpha1.ToolServer{})
			}, "10s", "1s").Should(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-tool-server",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			httpRoute := &gatewayv1.HTTPRoute{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-tool-server",
					Namespace: "default",
				}, httpRoute)
			}, "10s", "1s").Should(Succeed())

			Expect(httpRoute.Spec.ParentRefs).To(HaveLen(1))
			Expect(httpRoute.Spec.ParentRefs[0].Name).To(Equal(gatewayv1.ObjectName("test-gateway")))
			Expect(httpRoute.Spec.Rules).To(HaveLen(1))
			Expect(httpRoute.Spec.Rules[0].Matches).To(HaveLen(1))
			Expect(httpRoute.Spec.Rules[0].Matches[0].Path.Value).To(Equal(stringPtr("/mcp")))

			Expect(httpRoute.OwnerReferences).To(HaveLen(1))
			Expect(httpRoute.OwnerReferences[0].Name).To(Equal("test-tool-server"))
			Expect(httpRoute.OwnerReferences[0].Kind).To(Equal("ToolServer"))
		})

		It("should create HTTPRoute for SSE transport with /sse path", func() {
			setupTestGatewayAndClass("sse-test-class", "sse-test-gateway")

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

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-sse-server",
					Namespace: "default",
				}, &agentruntimev1alpha1.ToolServer{})
			}, "10s", "1s").Should(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-sse-server",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			httpRoute := &gatewayv1.HTTPRoute{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-sse-server",
					Namespace: "default",
				}, httpRoute)
			}, "10s", "1s").Should(Succeed())

			Expect(httpRoute.Spec.Rules[0].Matches[0].Path.Value).To(Equal(stringPtr("/sse")))
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

func stringPtr(s string) *string {
	return &s
}
