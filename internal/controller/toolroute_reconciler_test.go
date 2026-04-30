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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

var _ = Describe("ToolRouteReconciler", func() {
	ctx := context.Background()
	const ns = "default"

	var reconciler *ToolRouteReconciler

	BeforeEach(func() {
		reconciler = &ToolRouteReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: kevents.NewFakeRecorder(100),
		}
		createDefaultToolGatewayClass(ctx, k8sClient)
	})

	AfterEach(func() {
		cleanupTestResources(ctx, k8sClient, ns)
		cleanupToolGatewayClasses(ctx, k8sClient)
	})

	// reconcile is a helper that waits for resources to be visible in the cached
	// client and retries the reconcile until it succeeds.
	reconcileRoute := func(name string) {
		Eventually(func(g Gomega) {
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}})
			g.Expect(err).NotTo(HaveOccurred())
		}, "5s", "200ms").Should(Succeed())
	}

	It("skips a ToolRoute whose ToolGateway class is not owned by this controller", func() {
		foreignClass := &agentruntimev1alpha1.ToolGatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "tr-other-class"},
			Spec:       agentruntimev1alpha1.ToolGatewayClassSpec{Controller: "example.com/other"},
		}
		Expect(k8sClient.Create(ctx, foreignClass)).To(Succeed())

		tg := &agentruntimev1alpha1.ToolGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "foreign-tg", Namespace: ns},
			Spec:       agentruntimev1alpha1.ToolGatewaySpec{ToolGatewayClassName: "tr-other-class"},
		}
		Expect(k8sClient.Create(ctx, tg)).To(Succeed())

		tr := &agentruntimev1alpha1.ToolRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "tr-skip", Namespace: ns},
			Spec: agentruntimev1alpha1.ToolRouteSpec{
				ToolGatewayRef: &corev1.ObjectReference{Name: "foreign-tg"},
				Upstream: agentruntimev1alpha1.ToolRouteUpstream{
					External: &agentruntimev1alpha1.ExternalUpstream{Url: "https://example.com/mcp"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())

		// Wait for ToolRoute to be visible in cache, then reconcile.
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: tr.Name, Namespace: ns}, &agentruntimev1alpha1.ToolRoute{})
		}, "5s", "200ms").Should(Succeed())
		reconcileRoute(tr.Name)

		backend := &unstructured.Unstructured{}
		backend.SetAPIVersion("agentgateway.dev/v1alpha1")
		backend.SetKind("AgentgatewayBackend")
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "foreign-tg-tr-skip", Namespace: ns}, backend)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("creates an AgentgatewayBackend and HTTPRoute for a cluster ToolServer upstream", func() {
		tg := &agentruntimev1alpha1.ToolGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "own-tg", Namespace: ns},
		}
		Expect(k8sClient.Create(ctx, tg)).To(Succeed())

		ts := &agentruntimev1alpha1.ToolServer{
			ObjectMeta: metav1.ObjectMeta{Name: "ts-1", Namespace: ns},
			Spec: agentruntimev1alpha1.ToolServerSpec{
				Protocol:      "mcp",
				TransportType: "http",
				Image:         "example/mcp:latest",
				Port:          8080,
				Path:          "/mcp",
			},
		}
		Expect(k8sClient.Create(ctx, ts)).To(Succeed())

		tr := &agentruntimev1alpha1.ToolRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "tr-cluster", Namespace: ns},
			Spec: agentruntimev1alpha1.ToolRouteSpec{
				ToolGatewayRef: &corev1.ObjectReference{Name: "own-tg"},
				Upstream: agentruntimev1alpha1.ToolRouteUpstream{
					ToolServerRef: &corev1.ObjectReference{Name: "ts-1"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())

		Eventually(func(g Gomega) {
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: tr.Name, Namespace: ns}})
			g.Expect(err).NotTo(HaveOccurred())
			backend := &unstructured.Unstructured{}
			backend.SetAPIVersion("agentgateway.dev/v1alpha1")
			backend.SetKind("AgentgatewayBackend")
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "own-tg-tr-cluster", Namespace: ns}, backend)).To(Succeed())
			targets, found, err := unstructured.NestedSlice(backend.Object, "spec", "mcp", "targets")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(found).To(BeTrue())
			g.Expect(targets).To(HaveLen(1))
			target := targets[0].(map[string]interface{})
			static := target["static"].(map[string]interface{})
			g.Expect(static["host"]).To(Equal("ts-1.default.svc.cluster.local"))
			g.Expect(static["port"]).To(Equal(int64(8080)))
			g.Expect(static["path"]).To(Equal("/mcp"))
		}, "10s", "200ms").Should(Succeed())

		hr := &unstructured.Unstructured{}
		hr.SetAPIVersion("gateway.networking.k8s.io/v1")
		hr.SetKind("HTTPRoute")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "own-tg-tr-cluster", Namespace: ns}, hr)).To(Succeed())

		updated := &agentruntimev1alpha1.ToolRoute{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "tr-cluster", Namespace: ns}, updated)).To(Succeed())
		Expect(updated.Status.Url).To(ContainSubstring("/default/tr-cluster/mcp"))
	})

	It("creates a backend with a static host for an external upstream", func() {
		tg := &agentruntimev1alpha1.ToolGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "own-tg-ext", Namespace: ns},
		}
		Expect(k8sClient.Create(ctx, tg)).To(Succeed())

		tr := &agentruntimev1alpha1.ToolRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "tr-external", Namespace: ns},
			Spec: agentruntimev1alpha1.ToolRouteSpec{
				ToolGatewayRef: &corev1.ObjectReference{Name: "own-tg-ext"},
				Upstream: agentruntimev1alpha1.ToolRouteUpstream{
					External: &agentruntimev1alpha1.ExternalUpstream{Url: "https://github-mcp.example.com/mcp"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())

		Eventually(func(g Gomega) {
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: tr.Name, Namespace: ns}})
			g.Expect(err).NotTo(HaveOccurred())
			backend := &unstructured.Unstructured{}
			backend.SetAPIVersion("agentgateway.dev/v1alpha1")
			backend.SetKind("AgentgatewayBackend")
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "own-tg-ext-tr-external", Namespace: ns}, backend)).To(Succeed())
			targets, _, _ := unstructured.NestedSlice(backend.Object, "spec", "mcp", "targets")
			g.Expect(targets).To(HaveLen(1))
			target := targets[0].(map[string]interface{})
			static := target["static"].(map[string]interface{})
			g.Expect(static["host"]).To(Equal("github-mcp.example.com"))
			g.Expect(static["port"]).To(Equal(int64(443)))
			g.Expect(static["path"]).To(Equal("/mcp"))
		}, "10s", "200ms").Should(Succeed())
	})

	It("creates an AgentgatewayPolicy with CEL rules when toolFilter is set, and deletes it when filter is cleared", func() {
		tg := &agentruntimev1alpha1.ToolGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "own-tg-filter", Namespace: ns},
		}
		Expect(k8sClient.Create(ctx, tg)).To(Succeed())

		tr := &agentruntimev1alpha1.ToolRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "tr-filtered", Namespace: ns},
			Spec: agentruntimev1alpha1.ToolRouteSpec{
				ToolGatewayRef: &corev1.ObjectReference{Name: "own-tg-filter"},
				Upstream: agentruntimev1alpha1.ToolRouteUpstream{
					External: &agentruntimev1alpha1.ExternalUpstream{Url: "https://example.com/mcp"},
				},
				ToolFilter: &agentruntimev1alpha1.ToolFilter{
					Allow: []string{"get_*"},
					Deny:  []string{"force_push"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())

		Eventually(func(g Gomega) {
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: tr.Name, Namespace: ns}})
			g.Expect(err).NotTo(HaveOccurred())
			policy := &unstructured.Unstructured{}
			policy.SetAPIVersion("agentgateway.dev/v1alpha1")
			policy.SetKind("AgentgatewayPolicy")
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "own-tg-filter-tr-filtered-toolfilter", Namespace: ns}, policy)).To(Succeed())
			action, _, err := unstructured.NestedString(policy.Object, "spec", "backend", "mcp", "authorization", "action")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(action).To(Equal("Allow"))
			matchExprs, found, err := unstructured.NestedStringSlice(policy.Object, "spec", "backend", "mcp", "authorization", "policy", "matchExpressions")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(found).To(BeTrue())
			g.Expect(matchExprs).To(ConsistOf(`(mcp.tool.name.matches("^get_.*$")) && !(mcp.tool.name == "force_push")`))
			targetRefs, _, _ := unstructured.NestedSlice(policy.Object, "spec", "targetRefs")
			g.Expect(targetRefs).To(HaveLen(1))
			g.Expect(targetRefs[0].(map[string]interface{})["kind"]).To(Equal("HTTPRoute"))
			g.Expect(targetRefs[0].(map[string]interface{})["name"]).To(Equal("own-tg-filter-tr-filtered"))
		}, "10s", "200ms").Should(Succeed())

		// Clear the filter and re-reconcile; the policy should be deleted.
		updated := &agentruntimev1alpha1.ToolRoute{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "tr-filtered", Namespace: ns}, updated)).To(Succeed())
		updated.Spec.ToolFilter = nil
		Expect(k8sClient.Update(ctx, updated)).To(Succeed())

		Eventually(func(g Gomega) {
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: tr.Name, Namespace: ns}})
			g.Expect(err).NotTo(HaveOccurred())
			check := &unstructured.Unstructured{}
			check.SetAPIVersion("agentgateway.dev/v1alpha1")
			check.SetKind("AgentgatewayPolicy")
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "own-tg-filter-tr-filtered-toolfilter", Namespace: ns}, check)
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}, "10s", "200ms").Should(Succeed())
	})
})
