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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	kevents "k8s.io/client-go/tools/events"
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
			Recorder: kevents.NewFakeRecorder(100),
		}
	})

	AfterEach(func() {
		cleanupTestResources(ctx, k8sClient, "default")
	})

	// Helper to set Status.ToolGatewayRef on a ToolServer
	setToolGatewayRefStatus := func(name, namespace, gatewayName, gatewayNamespace string) {
		ts := &agentruntimev1alpha1.ToolServer{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, ts)).To(Succeed())
		ts.Status.ToolGatewayRef = &corev1.ObjectReference{
			Name:      gatewayName,
			Namespace: gatewayNamespace,
		}
		Expect(k8sClient.Status().Update(ctx, ts)).To(Succeed())
	}

	Describe("Reconcile", func() {
		It("should create HTTPRoute for HTTP transport", func() {
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
				},
			}
			Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-tool-server",
					Namespace: "default",
				}, &agentruntimev1alpha1.ToolServer{})
			}, "10s", "1s").Should(Succeed())

			setToolGatewayRefStatus("test-tool-server", "default", "test-gateway", "default")

			// Wait for the cache to reflect the status update
			Eventually(func() bool {
				ts := &agentruntimev1alpha1.ToolServer{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-tool-server", Namespace: "default"}, ts)
				return ts.Status.ToolGatewayRef != nil
			}, "10s", "1s").Should(BeTrue())

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
					Name:      "test-gateway-test-tool-server",
					Namespace: "default",
				}, httpRoute)
			}, "10s", "1s").Should(Succeed())

			Expect(httpRoute.Spec.ParentRefs).To(HaveLen(1))
			Expect(httpRoute.Spec.ParentRefs[0].Name).To(Equal(gatewayv1.ObjectName("test-gateway")))
			Expect(httpRoute.Spec.Rules).To(HaveLen(1))
			Expect(httpRoute.Spec.Rules[0].Matches).To(HaveLen(1))
			Expect(httpRoute.Spec.Rules[0].Matches[0].Path.Value).To(Equal(stringPtr("/default/test-tool-server/mcp")))

			Expect(httpRoute.OwnerReferences).To(HaveLen(1))
			Expect(httpRoute.OwnerReferences[0].Name).To(Equal("test-tool-server"))
			Expect(httpRoute.OwnerReferences[0].Kind).To(Equal("ToolServer"))
		})

		It("should create HTTPRoute for SSE transport with /sse path", func() {
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
				},
			}
			Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-sse-server",
					Namespace: "default",
				}, &agentruntimev1alpha1.ToolServer{})
			}, "10s", "1s").Should(Succeed())

			setToolGatewayRefStatus("test-sse-server", "default", "sse-test-gateway", "default")

			// Wait for the cache to reflect the status update
			Eventually(func() bool {
				ts := &agentruntimev1alpha1.ToolServer{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-sse-server", Namespace: "default"}, ts)
				return ts.Status.ToolGatewayRef != nil
			}, "10s", "1s").Should(BeTrue())

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
					Name:      "sse-test-gateway-test-sse-server",
					Namespace: "default",
				}, httpRoute)
			}, "10s", "1s").Should(Succeed())

			Expect(httpRoute.Spec.Rules[0].Matches[0].Path.Value).To(Equal(stringPtr("/default/test-sse-server/sse")))
		})

		It("should skip reconciliation when no ToolGatewayRef in status", func() {
			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-no-ref-server",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolServerSpec{
					Image:         "test-image:latest",
					Port:          8000,
					Protocol:      "mcp",
					TransportType: "http",
				},
			}
			Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-no-ref-server",
					Namespace: "default",
				}, &agentruntimev1alpha1.ToolServer{})
			}, "10s", "1s").Should(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-no-ref-server",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// HTTPRoute should NOT be created since Status.ToolGatewayRef is nil —
			// there is no gateway name to derive the route name from.
			routeList := &gatewayv1.HTTPRouteList{}
			Expect(k8sClient.List(ctx, routeList, client.InNamespace("default"))).To(Succeed())
			for _, r := range routeList.Items {
				Expect(r.Name).NotTo(ContainSubstring("test-no-ref-server"))
			}
		})

		It("should update HTTPRoute path when ToolServer TransportType changes", func() {
			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-transport",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolServerSpec{
					Image:         "test-image:latest",
					Port:          8000,
					Protocol:      "mcp",
					TransportType: "http",
				},
			}
			Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-update-transport", Namespace: "default"}, &agentruntimev1alpha1.ToolServer{})
			}, "10s", "1s").Should(Succeed())

			setToolGatewayRefStatus("test-update-transport", "default", "test-gateway", "default")

			Eventually(func() bool {
				ts := &agentruntimev1alpha1.ToolServer{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-update-transport", Namespace: "default"}, ts)
				return ts.Status.ToolGatewayRef != nil
			}, "10s", "1s").Should(BeTrue())

			// First reconcile – creates HTTPRoute with /mcp path
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-update-transport", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			httpRoute := &gatewayv1.HTTPRoute{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-gateway-test-update-transport", Namespace: "default"}, httpRoute)
			}, "10s", "1s").Should(Succeed())
			Expect(httpRoute.Spec.Rules[0].Matches[0].Path.Value).To(Equal(stringPtr("/default/test-update-transport/mcp")))

			// Update TransportType to sse
			ts := &agentruntimev1alpha1.ToolServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-update-transport", Namespace: "default"}, ts)).To(Succeed())
			patch := client.MergeFrom(ts.DeepCopy())
			ts.Spec.TransportType = "sse"
			Expect(k8sClient.Patch(ctx, ts, patch)).To(Succeed())

			Eventually(func() string {
				updated := &agentruntimev1alpha1.ToolServer{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-update-transport", Namespace: "default"}, updated)
				return updated.Spec.TransportType
			}, "10s", "1s").Should(Equal("sse"))

			// Second reconcile – should update HTTPRoute path to /sse
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-update-transport", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			updatedRoute := &gatewayv1.HTTPRoute{}
			Eventually(func() *string {
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-gateway-test-update-transport", Namespace: "default"}, updatedRoute)
				if len(updatedRoute.Spec.Rules) == 0 || len(updatedRoute.Spec.Rules[0].Matches) == 0 {
					return nil
				}
				return updatedRoute.Spec.Rules[0].Matches[0].Path.Value
			}, "10s", "1s").Should(Equal(stringPtr("/default/test-update-transport/sse")))
		})

		It("should update HTTPRoute parent ref when ToolGatewayRef changes", func() {
			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-gatewayref",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolServerSpec{
					Image:         "test-image:latest",
					Port:          8000,
					Protocol:      "mcp",
					TransportType: "http",
				},
			}
			Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-update-gatewayref", Namespace: "default"}, &agentruntimev1alpha1.ToolServer{})
			}, "10s", "1s").Should(Succeed())

			setToolGatewayRefStatus("test-update-gatewayref", "default", "gateway-a", "default")

			Eventually(func() bool {
				ts := &agentruntimev1alpha1.ToolServer{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-update-gatewayref", Namespace: "default"}, ts)
				return ts.Status.ToolGatewayRef != nil
			}, "10s", "1s").Should(BeTrue())

			// First reconcile – creates HTTPRoute pointing at gateway-a
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-update-gatewayref", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			httpRoute := &gatewayv1.HTTPRoute{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "gateway-a-test-update-gatewayref", Namespace: "default"}, httpRoute)
			}, "10s", "1s").Should(Succeed())
			Expect(httpRoute.Spec.ParentRefs[0].Name).To(Equal(gatewayv1.ObjectName("gateway-a")))

			// Update ToolGatewayRef to point at gateway-b
			ts := &agentruntimev1alpha1.ToolServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-update-gatewayref", Namespace: "default"}, ts)).To(Succeed())
			ts.Status.ToolGatewayRef = &corev1.ObjectReference{
				Name:      "gateway-b",
				Namespace: "default",
			}
			Expect(k8sClient.Status().Update(ctx, ts)).To(Succeed())

			Eventually(func() string {
				updated := &agentruntimev1alpha1.ToolServer{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-update-gatewayref", Namespace: "default"}, updated)
				if updated.Status.ToolGatewayRef == nil {
					return ""
				}
				return updated.Status.ToolGatewayRef.Name
			}, "10s", "1s").Should(Equal("gateway-b"))

			// Second reconcile – creates a new route named gateway-b-test-update-gatewayref
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-update-gatewayref", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			updatedRoute := &gatewayv1.HTTPRoute{}
			Eventually(func() gatewayv1.ObjectName {
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "gateway-b-test-update-gatewayref", Namespace: "default"}, updatedRoute)
				if len(updatedRoute.Spec.ParentRefs) == 0 {
					return ""
				}
				return updatedRoute.Spec.ParentRefs[0].Name
			}, "10s", "1s").Should(Equal(gatewayv1.ObjectName("gateway-b")))
		})

		It("should update AgentgatewayBackend when ToolServer port changes", func() {
			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-port",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolServerSpec{
					Image:         "test-image:latest",
					Port:          8000,
					Protocol:      "mcp",
					TransportType: "http",
				},
			}
			Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-update-port", Namespace: "default"}, &agentruntimev1alpha1.ToolServer{})
			}, "10s", "1s").Should(Succeed())

			setToolGatewayRefStatus("test-update-port", "default", "test-gateway", "default")

			Eventually(func() bool {
				ts := &agentruntimev1alpha1.ToolServer{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-update-port", Namespace: "default"}, ts)
				return ts.Status.ToolGatewayRef != nil
			}, "10s", "1s").Should(BeTrue())

			// First reconcile – creates AgentgatewayBackend with port 8000
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-update-port", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			backend := &unstructured.Unstructured{}
			backend.SetAPIVersion("agentgateway.dev/v1alpha1")
			backend.SetKind("AgentgatewayBackend")
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-gateway-test-update-port", Namespace: "default"}, backend)
			}, "10s", "1s").Should(Succeed())

			targets, _, _ := unstructured.NestedSlice(backend.Object, "spec", "mcp", "targets")
			Expect(targets).To(HaveLen(1))
			firstTarget := targets[0].(map[string]interface{})
			Expect(firstTarget["static"].(map[string]interface{})["port"]).To(Equal(int64(8000)))

			// Update ToolServer port to 9000
			ts := &agentruntimev1alpha1.ToolServer{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-update-port", Namespace: "default"}, ts)).To(Succeed())
			patch := client.MergeFrom(ts.DeepCopy())
			ts.Spec.Port = 9000
			Expect(k8sClient.Patch(ctx, ts, patch)).To(Succeed())

			Eventually(func() int32 {
				updated := &agentruntimev1alpha1.ToolServer{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-update-port", Namespace: "default"}, updated)
				return updated.Spec.Port
			}, "10s", "1s").Should(Equal(int32(9000)))

			// Second reconcile – should update AgentgatewayBackend port to 9000
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-update-port", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			updatedBackend := &unstructured.Unstructured{}
			updatedBackend.SetAPIVersion("agentgateway.dev/v1alpha1")
			updatedBackend.SetKind("AgentgatewayBackend")
			Eventually(func() int64 {
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-gateway-test-update-port", Namespace: "default"}, updatedBackend)
				targets, _, _ := unstructured.NestedSlice(updatedBackend.Object, "spec", "mcp", "targets")
				if len(targets) == 0 {
					return 0
				}
				port, _, _ := unstructured.NestedInt64(
					targets[0].(map[string]interface{}),
					"static", "port",
				)
				return port
			}, "10s", "1s").Should(Equal(int64(9000)))
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

})

func stringPtr(s string) *string {
	return &s
}
