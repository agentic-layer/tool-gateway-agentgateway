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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

var _ = Describe("ToolGateway Controller", func() {
	ctx := context.Background()
	var reconciler *ToolGatewayReconciler

	BeforeEach(func() {
		reconciler = &ToolGatewayReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(100),
		}
	})

	AfterEach(func() {
		cleanupTestResources(ctx, k8sClient, "default")
	})

	Describe("Reconcile", func() {
		It("should create ToolGatewayClass", func() {
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-class",
				},
				Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
					Controller: "runtime.agentic-layer.ai/tool-gateway-agentgateway-controller",
				},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())
		})

		It("should create ToolGateway and reconcile to create Gateway", func() {
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-class-2",
				},
				Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
					Controller: "runtime.agentic-layer.ai/tool-gateway-agentgateway-controller",
				},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-basic-gateway",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "test-class-2",
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-basic-gateway",
					Namespace: "default",
				}, &agentruntimev1alpha1.ToolGateway{})
			}, "10s", "1s").Should(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-basic-gateway",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			gateway := &gatewayv1.Gateway{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-basic-gateway",
					Namespace: "default",
				}, gateway)
			}, "10s", "1s").Should(Succeed())

			Expect(gateway.Spec.GatewayClassName).To(Equal(gatewayv1.ObjectName("agentgateway")))
			Expect(gateway.Spec.Listeners).To(HaveLen(1))
			Expect(gateway.Spec.Listeners[0].Protocol).To(Equal(gatewayv1.HTTPProtocolType))
			Expect(gateway.Spec.Listeners[0].Port).To(Equal(gatewayv1.PortNumber(80)))
			Expect(gateway.Spec.Listeners[0].AllowedRoutes).NotTo(BeNil())
			Expect(gateway.Spec.Listeners[0].AllowedRoutes.Namespaces.From).NotTo(BeNil())
			Expect(*gateway.Spec.Listeners[0].AllowedRoutes.Namespaces.From).To(Equal(gatewayv1.NamespacesFromAll))

			Expect(gateway.OwnerReferences).To(HaveLen(1))
			Expect(gateway.OwnerReferences[0].Name).To(Equal("test-basic-gateway"))
			Expect(gateway.OwnerReferences[0].Kind).To(Equal("ToolGateway"))

			// Check status URL and Ready condition
			updated := &agentruntimev1alpha1.ToolGateway{}
			Eventually(func() bool {
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-basic-gateway", Namespace: "default"}, updated)
				return apimeta.IsStatusConditionTrue(updated.Status.Conditions, "Ready")
			}, "10s", "1s").Should(BeTrue())
			Expect(updated.Status.Url).To(Equal("http://test-basic-gateway.default.svc.cluster.local"))

			// Second reconcile should be idempotent
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-basic-gateway",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should restore Gateway to desired state after drift", func() {
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-class-drift",
				},
				Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
					Controller: "runtime.agentic-layer.ai/tool-gateway-agentgateway-controller",
				},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-drift-gateway",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "test-class-drift",
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-drift-gateway", Namespace: "default"}, &agentruntimev1alpha1.ToolGateway{})
			}, "10s", "1s").Should(Succeed())

			// First reconcile – creates Gateway
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-drift-gateway", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			gateway := &gatewayv1.Gateway{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-drift-gateway", Namespace: "default"}, gateway)
			}, "10s", "1s").Should(Succeed())

			// Simulate drift: change the listener port to something wrong
			patch := client.MergeFrom(gateway.DeepCopy())
			gateway.Spec.Listeners[0].Port = gatewayv1.PortNumber(9090)
			Expect(k8sClient.Patch(ctx, gateway, patch)).To(Succeed())

			Eventually(func() gatewayv1.PortNumber {
				gw := &gatewayv1.Gateway{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-drift-gateway", Namespace: "default"}, gw)
				if len(gw.Spec.Listeners) == 0 {
					return 0
				}
				return gw.Spec.Listeners[0].Port
			}, "10s", "1s").Should(Equal(gatewayv1.PortNumber(9090)))

			// Second reconcile – should restore the listener
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-drift-gateway", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			restored := &gatewayv1.Gateway{}
			Eventually(func() int {
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-drift-gateway", Namespace: "default"}, restored)
				return len(restored.Spec.Listeners)
			}, "10s", "1s").Should(Equal(1))
			Expect(restored.Spec.Listeners[0].Protocol).To(Equal(gatewayv1.HTTPProtocolType))
			Expect(restored.Spec.Listeners[0].Port).To(Equal(gatewayv1.PortNumber(80)))
		})

		It("should return nil when ToolGateway is not found", func() {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "nonexistent-gateway",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should not reconcile ToolGateway with wrong controller", func() {
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "wrong-controller-class",
				},
				Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
					Controller: "other-controller",
				},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-wrong-controller",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "wrong-controller-class",
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-wrong-controller",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			gateway := &gatewayv1.Gateway{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      "test-wrong-controller",
				Namespace: "default",
			}, gateway)
			Expect(err).To(HaveOccurred())
		})
	})
})
