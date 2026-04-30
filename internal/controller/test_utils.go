/*
Copyright 2025.

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

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
	"github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// defaultTestToolGatewayClassName matches the class shipped by the operator
// install kustomization (config/install/toolgatewayclass.yaml). Tests use the
// same name so that class lookups behave like a real cluster.
const defaultTestToolGatewayClassName = "agentgateway"

// createDefaultToolGatewayClass installs the production-realistic default
// ToolGatewayClass: the conventional name, this operator's controller string,
// and the is-default-class annotation. Any ToolGateway without an explicit
// ToolGatewayClassName picks this one up.
func createDefaultToolGatewayClass(ctx context.Context, k8sClient client.Client) {
	class := &agentruntimev1alpha1.ToolGatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: defaultTestToolGatewayClassName,
			Annotations: map[string]string{
				toolGatewayClassDefaultAnnotation: "true",
			},
		},
		Spec: agentruntimev1alpha1.ToolGatewayClassSpec{
			Controller: ToolGatewayAgentgatewayControllerName,
		},
	}
	gomega.Expect(k8sClient.Create(ctx, class)).To(gomega.Succeed())
}

// cleanupToolGatewayClasses removes every ToolGatewayClass in the cluster.
// ToolGatewayClasses are cluster-scoped, so this is the equivalent of
// cleanupTestResources for that kind. Safe to call when none exist.
func cleanupToolGatewayClasses(ctx context.Context, k8sClient client.Client) {
	classList := &agentruntimev1alpha1.ToolGatewayClassList{}
	gomega.Expect(k8sClient.List(ctx, classList)).To(gomega.Succeed())
	for i := range classList.Items {
		err := k8sClient.Delete(ctx, &classList.Items[i])
		if err != nil && !apierrors.IsNotFound(err) {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	}
	// Wait for deletions to settle so the next test starts from a clean slate.
	gomega.Eventually(func() int {
		remaining := &agentruntimev1alpha1.ToolGatewayClassList{}
		_ = k8sClient.List(ctx, remaining)
		return len(remaining.Items)
	}, "5s", "100ms").Should(gomega.Equal(0))
}

// cleanupTestResources cleans up all test resources in the specified namespace.
// This is a shared utility function used by both ToolGateway and ToolRoute tests.
func cleanupTestResources(ctx context.Context, k8sClient client.Client, namespace string) {
	// Clean up all tool routes in the namespace
	toolRouteList := &agentruntimev1alpha1.ToolRouteList{}
	gomega.Expect(k8sClient.List(ctx, toolRouteList, &client.ListOptions{Namespace: namespace})).To(gomega.Succeed())
	for i := range toolRouteList.Items {
		_ = k8sClient.Delete(ctx, &toolRouteList.Items[i])
	}

	// Clean up all tool servers in the namespace
	toolServerList := &agentruntimev1alpha1.ToolServerList{}
	gomega.Expect(k8sClient.List(ctx, toolServerList, &client.ListOptions{Namespace: namespace})).To(gomega.Succeed())
	for i := range toolServerList.Items {
		_ = k8sClient.Delete(ctx, &toolServerList.Items[i])
	}

	// Clean up all tool gateways in the namespace
	toolGatewayList := &agentruntimev1alpha1.ToolGatewayList{}
	gomega.Expect(k8sClient.List(ctx, toolGatewayList, &client.ListOptions{Namespace: namespace})).To(gomega.Succeed())
	for i := range toolGatewayList.Items {
		_ = k8sClient.Delete(ctx, &toolGatewayList.Items[i])
	}

	// Clean up all HTTPRoutes in the namespace
	httpRouteList := &gatewayv1.HTTPRouteList{}
	gomega.Expect(k8sClient.List(ctx, httpRouteList, &client.ListOptions{Namespace: namespace})).To(gomega.Succeed())
	for i := range httpRouteList.Items {
		_ = k8sClient.Delete(ctx, &httpRouteList.Items[i])
	}

	// Clean up all AgentgatewayParameters in the namespace
	agentgatewayParamsList := &unstructured.UnstructuredList{}
	agentgatewayParamsList.SetAPIVersion("agentgateway.dev/v1alpha1")
	agentgatewayParamsList.SetKind("AgentgatewayParametersList")
	gomega.Expect(k8sClient.List(ctx, agentgatewayParamsList, &client.ListOptions{Namespace: namespace})).To(gomega.Succeed())
	for i := range agentgatewayParamsList.Items {
		_ = k8sClient.Delete(ctx, &agentgatewayParamsList.Items[i])
	}

	// Clean up all AgentgatewayBackends in the namespace
	agentgatewayBackendList := &unstructured.UnstructuredList{}
	agentgatewayBackendList.SetAPIVersion("agentgateway.dev/v1alpha1")
	agentgatewayBackendList.SetKind("AgentgatewayBackendList")
	gomega.Expect(k8sClient.List(ctx, agentgatewayBackendList, &client.ListOptions{Namespace: namespace})).To(gomega.Succeed())
	for i := range agentgatewayBackendList.Items {
		_ = k8sClient.Delete(ctx, &agentgatewayBackendList.Items[i])
	}

	// Clean up all AgentgatewayPolicies in the namespace
	agentgatewayPolicyList := &unstructured.UnstructuredList{}
	agentgatewayPolicyList.SetAPIVersion("agentgateway.dev/v1alpha1")
	agentgatewayPolicyList.SetKind("AgentgatewayPolicyList")
	gomega.Expect(k8sClient.List(ctx, agentgatewayPolicyList, &client.ListOptions{Namespace: namespace})).To(gomega.Succeed())
	for i := range agentgatewayPolicyList.Items {
		_ = k8sClient.Delete(ctx, &agentgatewayPolicyList.Items[i])
	}
}
