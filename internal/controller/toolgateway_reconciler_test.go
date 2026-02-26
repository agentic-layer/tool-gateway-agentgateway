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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	kevents "k8s.io/client-go/tools/events"
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
			Recorder: kevents.NewFakeRecorder(100),
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

			Expect(gateway.Spec.Infrastructure).NotTo(BeNil())
			Expect(gateway.Spec.Infrastructure.ParametersRef).NotTo(BeNil())
			Expect(string(gateway.Spec.Infrastructure.ParametersRef.Group)).To(Equal("agentgateway.dev"))
			Expect(string(gateway.Spec.Infrastructure.ParametersRef.Kind)).To(Equal("AgentgatewayParameters"))
			Expect(gateway.Spec.Infrastructure.ParametersRef.Name).To(Equal("test-basic-gateway"))

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

		It("should create multiplex routes when ToolServers are available", func() {
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-class-multiplex",
				},
				Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
					Controller: "runtime.agentic-layer.ai/tool-gateway-agentgateway-controller",
				},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-multiplex-gateway",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "test-class-multiplex",
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

			// Create a ToolServer that references this gateway
			toolServer := &agentruntimev1alpha1.ToolServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-mcp-server",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolServerSpec{
					Protocol:      "mcp",
					TransportType: "http",
					Image:         "test-image",
					Port:          8000,
					Path:          "/mcp",
				},
			}
			Expect(k8sClient.Create(ctx, toolServer)).To(Succeed())

			// Set ToolGatewayRef in status
			Eventually(func() error {
				ts := &agentruntimev1alpha1.ToolServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-mcp-server", Namespace: "default"}, ts); err != nil {
					return err
				}
				ts.Status.ToolGatewayRef = &corev1.ObjectReference{
					Name:      "test-multiplex-gateway",
					Namespace: "default",
				}
				return k8sClient.Status().Update(ctx, ts)
			}, "10s", "1s").Should(Succeed())

			// Reconcile ToolGateway
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-multiplex-gateway",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Check that multiplex routes were created
			// Root route (/mcp) — named after the ToolGateway, in the gateway namespace
			rootRoute := &gatewayv1.HTTPRoute{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-multiplex-gateway",
					Namespace: "default",
				}, rootRoute)
			}, "10s", "1s").Should(Succeed())
			Expect(rootRoute.Spec.Rules).To(HaveLen(1))
			Expect(rootRoute.Spec.Rules[0].Matches).To(HaveLen(1))
			Expect(*rootRoute.Spec.Rules[0].Matches[0].Path.Value).To(Equal("/mcp"))

			// Namespace route (/<namespace>/mcp) — named <gateway>-<ns>, in the ToolServer namespace
			nsRoute := &gatewayv1.HTTPRoute{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-multiplex-gateway-default",
					Namespace: "default",
				}, nsRoute)
			}, "10s", "1s").Should(Succeed())
			Expect(nsRoute.Spec.Rules).To(HaveLen(1))
			Expect(nsRoute.Spec.Rules[0].Matches).To(HaveLen(1))
			Expect(*nsRoute.Spec.Rules[0].Matches[0].Path.Value).To(Equal("/default/mcp"))
		})

		It("should pass spec.env through to AgentgatewayParameters", func() {
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-class-env"},
				Spec:       agentruntimev1alpha1.ToolGatewayClassSpec{Controller: "runtime.agentic-layer.ai/tool-gateway-agentgateway-controller"},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "test-env-gateway", Namespace: "default"},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "test-class-env",
					Env: []corev1.EnvVar{
						{Name: "MY_VAR", Value: "my-value"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-env-gateway", Namespace: "default"}, &agentruntimev1alpha1.ToolGateway{})
			}, "10s", "1s").Should(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-env-gateway", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			params := &unstructured.Unstructured{}
			params.SetAPIVersion("agentgateway.dev/v1alpha1")
			params.SetKind("AgentgatewayParameters")
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-env-gateway", Namespace: "default"}, params)
			}, "10s", "1s").Should(Succeed())

			envVars, found, err := unstructured.NestedSlice(params.Object, "spec", "env")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(envVars).To(HaveLen(1))
			Expect(envVars[0].(map[string]interface{})["name"]).To(Equal("MY_VAR"))
			Expect(envVars[0].(map[string]interface{})["value"]).To(Equal("my-value"))

			Expect(params.GetOwnerReferences()).To(HaveLen(1))
			Expect(params.GetOwnerReferences()[0].Name).To(Equal("test-env-gateway"))
		})

		It("should pass spec.envFrom through to AgentgatewayParameters deployment spec", func() {
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-class-envfrom"},
				Spec:       agentruntimev1alpha1.ToolGatewayClassSpec{Controller: "runtime.agentic-layer.ai/tool-gateway-agentgateway-controller"},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "test-envfrom-gateway", Namespace: "default"},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "test-class-envfrom",
					EnvFrom: []corev1.EnvFromSource{
						{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "my-config"}}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-envfrom-gateway", Namespace: "default"}, &agentruntimev1alpha1.ToolGateway{})
			}, "10s", "1s").Should(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-envfrom-gateway", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			params := &unstructured.Unstructured{}
			params.SetAPIVersion("agentgateway.dev/v1alpha1")
			params.SetKind("AgentgatewayParameters")
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-envfrom-gateway", Namespace: "default"}, params)
			}, "10s", "1s").Should(Succeed())

			containers, found, err := unstructured.NestedSlice(params.Object, "spec", "deployment", "spec", "template", "spec", "containers")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(containers).To(HaveLen(1))
			container := containers[0].(map[string]interface{})
			Expect(container["name"]).To(Equal("agentgateway"))
			envFromList := container["envFrom"].([]interface{})
			Expect(envFromList).To(HaveLen(1))
			configMapRef := envFromList[0].(map[string]interface{})["configMapRef"].(map[string]interface{})
			Expect(configMapRef["name"]).To(Equal("my-config"))
		})

		It("should skip multiplex routes when no ToolServers are available", func() {
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-class-no-servers",
				},
				Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
					Controller: "runtime.agentic-layer.ai/tool-gateway-agentgateway-controller",
				},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-no-servers-gateway",
					Namespace: "default",
				},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "test-class-no-servers",
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())

			// Reconcile ToolGateway without any ToolServers
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-no-servers-gateway",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Multiplex routes should not be created
			rootRoute := &gatewayv1.HTTPRoute{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      "test-no-servers-gateway",
				Namespace: "default",
			}, rootRoute)
			Expect(err).To(HaveOccurred())
		})

		It("should set Service type to ClusterIP in AgentgatewayParameters", func() {
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-class-service"},
				Spec:       agentruntimev1alpha1.ToolGatewayClassSpec{Controller: "runtime.agentic-layer.ai/tool-gateway-agentgateway-controller"},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "test-service-gateway", Namespace: "default"},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "test-class-service",
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-service-gateway", Namespace: "default"}, &agentruntimev1alpha1.ToolGateway{})
			}, "10s", "1s").Should(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-service-gateway", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			params := &unstructured.Unstructured{}
			params.SetAPIVersion("agentgateway.dev/v1alpha1")
			params.SetKind("AgentgatewayParameters")
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-service-gateway", Namespace: "default"}, params)
			}, "10s", "1s").Should(Succeed())

			serviceType, found, err := unstructured.NestedString(params.Object, "spec", "service", "spec", "type")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(serviceType).To(Equal("ClusterIP"))
		})
	})
})
