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

		It("should update AgentgatewayParameters when spec.env changes", func() {
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-class-env-update"},
				Spec:       agentruntimev1alpha1.ToolGatewayClassSpec{Controller: "runtime.agentic-layer.ai/tool-gateway-agentgateway-controller"},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "test-env-update-gateway", Namespace: "default"},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "test-class-env-update",
					Env: []corev1.EnvVar{
						{Name: "MY_VAR", Value: "initial-value"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-env-update-gateway", Namespace: "default"}, &agentruntimev1alpha1.ToolGateway{})
			}, "10s", "1s").Should(Succeed())

			// First reconcile – creates AgentgatewayParameters with initial env var
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-env-update-gateway", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			params := &unstructured.Unstructured{}
			params.SetAPIVersion("agentgateway.dev/v1alpha1")
			params.SetKind("AgentgatewayParameters")
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-env-update-gateway", Namespace: "default"}, params)
			}, "10s", "1s").Should(Succeed())

			envVars, found, err := unstructured.NestedSlice(params.Object, "spec", "env")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(envVars).To(HaveLen(1))
			Expect(envVars[0].(map[string]interface{})["value"]).To(Equal("initial-value"))

			// Update the ToolGateway spec.env
			tg := &agentruntimev1alpha1.ToolGateway{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-env-update-gateway", Namespace: "default"}, tg)).To(Succeed())
			patch := client.MergeFrom(tg.DeepCopy())
			tg.Spec.Env = []corev1.EnvVar{{Name: "MY_VAR", Value: "updated-value"}}
			Expect(k8sClient.Patch(ctx, tg, patch)).To(Succeed())

			// Wait for the cache to reflect the updated ToolGateway spec
			Eventually(func() string {
				updated := &agentruntimev1alpha1.ToolGateway{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-env-update-gateway", Namespace: "default"}, updated)
				if len(updated.Spec.Env) == 0 {
					return ""
				}
				return updated.Spec.Env[0].Value
			}, "10s", "1s").Should(Equal("updated-value"))

			// Second reconcile – should update AgentgatewayParameters with new env var
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-env-update-gateway", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			updatedParams := &unstructured.Unstructured{}
			updatedParams.SetAPIVersion("agentgateway.dev/v1alpha1")
			updatedParams.SetKind("AgentgatewayParameters")
			Eventually(func() string {
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-env-update-gateway", Namespace: "default"}, updatedParams)
				vars, _, _ := unstructured.NestedSlice(updatedParams.Object, "spec", "env")
				if len(vars) == 0 {
					return ""
				}
				v, _ := vars[0].(map[string]interface{})["value"].(string)
				return v
			}, "10s", "1s").Should(Equal("updated-value"))
		})

		It("should clear spec.rawConfig in AgentgatewayParameters when OTEL env vars are removed", func() {
			toolGatewayClass := &agentruntimev1alpha1.ToolGatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-class-otel-remove"},
				Spec:       agentruntimev1alpha1.ToolGatewayClassSpec{Controller: "runtime.agentic-layer.ai/tool-gateway-agentgateway-controller"},
			}
			Expect(k8sClient.Create(ctx, toolGatewayClass)).To(Succeed())

			toolGateway := &agentruntimev1alpha1.ToolGateway{
				ObjectMeta: metav1.ObjectMeta{Name: "test-otel-remove-gateway", Namespace: "default"},
				Spec: agentruntimev1alpha1.ToolGatewaySpec{
					ToolGatewayClassName: "test-class-otel-remove",
					Env: []corev1.EnvVar{
						{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://otel-collector:4318"},
						{Name: "MY_VAR", Value: "my-value"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, toolGateway)).To(Succeed())
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-otel-remove-gateway", Namespace: "default"}, &agentruntimev1alpha1.ToolGateway{})
			}, "10s", "1s").Should(Succeed())

			// First reconcile – creates AgentgatewayParameters with OTEL config in rawConfig
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-otel-remove-gateway", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			params := &unstructured.Unstructured{}
			params.SetAPIVersion("agentgateway.dev/v1alpha1")
			params.SetKind("AgentgatewayParameters")
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "test-otel-remove-gateway", Namespace: "default"}, params)
			}, "10s", "1s").Should(Succeed())

			// Verify rawConfig is set
			_, found, err := unstructured.NestedMap(params.Object, "spec", "rawConfig")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			// Remove the OTEL env var from the ToolGateway spec
			tg := &agentruntimev1alpha1.ToolGateway{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-otel-remove-gateway", Namespace: "default"}, tg)).To(Succeed())
			patch := client.MergeFrom(tg.DeepCopy())
			tg.Spec.Env = []corev1.EnvVar{{Name: "MY_VAR", Value: "my-value"}}
			Expect(k8sClient.Patch(ctx, tg, patch)).To(Succeed())

			// Wait for the cache to reflect the updated ToolGateway spec (only MY_VAR remains)
			Eventually(func() int {
				updated := &agentruntimev1alpha1.ToolGateway{}
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-otel-remove-gateway", Namespace: "default"}, updated)
				return len(updated.Spec.Env)
			}, "10s", "1s").Should(Equal(1))

			// Second reconcile – should clear spec.rawConfig since no OTEL env vars remain
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "test-otel-remove-gateway", Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			updatedParams := &unstructured.Unstructured{}
			updatedParams.SetAPIVersion("agentgateway.dev/v1alpha1")
			updatedParams.SetKind("AgentgatewayParameters")
			Eventually(func() bool {
				_ = k8sClient.Get(ctx, types.NamespacedName{Name: "test-otel-remove-gateway", Namespace: "default"}, updatedParams)
				_, found, _ := unstructured.NestedMap(updatedParams.Object, "spec", "rawConfig")
				return found
			}, "10s", "1s").Should(BeFalse())
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
