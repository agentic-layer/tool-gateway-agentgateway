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
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// cleanupTestResources cleans up all test resources in the specified namespace.
// This is a shared utility function used by both ToolGateway and ToolServer tests.
func cleanupTestResources(ctx context.Context, k8sClient client.Client, namespace string) {
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

	// Clean up all tool gateway classes (cluster-scoped)
	toolGatewayClassList := &agentruntimev1alpha1.ToolGatewayClassList{}
	gomega.Expect(k8sClient.List(ctx, toolGatewayClassList)).To(gomega.Succeed())
	for i := range toolGatewayClassList.Items {
		_ = k8sClient.Delete(ctx, &toolGatewayClassList.Items[i])
	}

	// Clean up all HTTPRoutes in the namespace
	httpRouteList := &gatewayv1.HTTPRouteList{}
	gomega.Expect(k8sClient.List(ctx, httpRouteList, &client.ListOptions{Namespace: namespace})).To(gomega.Succeed())
	for i := range httpRouteList.Items {
		_ = k8sClient.Delete(ctx, &httpRouteList.Items[i])
	}
}
