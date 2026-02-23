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
					Name:      "test-tool-server",
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
					Name:      "test-sse-server",
					Namespace: "default",
				}, httpRoute)
			}, "10s", "1s").Should(Succeed())

			Expect(httpRoute.Spec.Rules[0].Matches[0].Path.Value).To(Equal(stringPtr("/default/test-sse-server/sse")))
		})

		It("should skip reconciliation when no ToolGatewayRef in status", func() {
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

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-tool-server",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// HTTPRoute should NOT be created since Status.ToolGatewayRef is nil
			httpRoute := &gatewayv1.HTTPRoute{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      "test-tool-server",
				Namespace: "default",
			}, httpRoute)
			Expect(err).To(HaveOccurred())
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
