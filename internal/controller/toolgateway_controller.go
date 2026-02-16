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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

const ToolGatewayKgatewayControllerName = "runtime.agentic-layer.ai/tool-gateway-kgateway-controller"

const (
	agentGatewayClassName    = "agentgateway"
	toolGatewayFinalizer     = "runtime.agentic-layer.ai/finalizer"
	toolGatewayLabel         = "tool-gateway.runtime.agentic-layer.ai/name"
	toolServerLabel          = "tool-server.runtime.agentic-layer.ai/name"
	toolServerNamespaceLabel = "tool-server.runtime.agentic-layer.ai/namespace"
)

// Version set at build time using ldflags
var Version = "dev"

// ToolGatewayReconciler reconciles a ToolGateway object
type ToolGatewayReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaybackends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ToolGatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the ToolGateway instance
	var toolGateway agentruntimev1alpha1.ToolGateway
	if err := r.Get(ctx, req.NamespacedName, &toolGateway); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ToolGateway resource not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ToolGateway")
		return ctrl.Result{}, err
	}

	log.Info("Reconciling ToolGateway",
		"name", toolGateway.Name,
		"namespace", toolGateway.Namespace,
		"toolGatewayClass", toolGateway.Spec.ToolGatewayClassName)

	// Check if this controller should process this ToolGateway
	if !r.shouldProcessToolGateway(ctx, &toolGateway) {
		log.Info("Controller is not responsible for this ToolGateway, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Handle finalizer for cleanup
	if toolGateway.DeletionTimestamp.IsZero() {
		// Object is not being deleted, ensure finalizer is present
		if !controllerutil.ContainsFinalizer(&toolGateway, toolGatewayFinalizer) {
			controllerutil.AddFinalizer(&toolGateway, toolGatewayFinalizer)
			if err := r.Update(ctx, &toolGateway); err != nil {
				log.Error(err, "Failed to add finalizer")
				return ctrl.Result{}, err
			}
			log.Info("Added finalizer to ToolGateway")
		}
	} else {
		// Object is being deleted
		if controllerutil.ContainsFinalizer(&toolGateway, toolGatewayFinalizer) {
			// Perform cleanup
			r.cleanupResources(ctx, &toolGateway)

			// Remove finalizer
			controllerutil.RemoveFinalizer(&toolGateway, toolGatewayFinalizer)
			if err := r.Update(ctx, &toolGateway); err != nil {
				log.Error(err, "Failed to remove finalizer")
				return ctrl.Result{}, err
			}
			log.Info("Removed finalizer from ToolGateway")
		}
		return ctrl.Result{}, nil
	}

	// Create or update the Gateway for this ToolGateway
	if err := r.ensureGateway(ctx, &toolGateway); err != nil {
		log.Error(err, "Failed to ensure Gateway")
		r.Recorder.Event(&toolGateway, "Warning", "GatewayFailed", err.Error())
		return ctrl.Result{}, err
	}

	// Get all ToolServer resources
	toolServers, err := r.getToolServers(ctx)
	if err != nil {
		log.Error(err, "Failed to get ToolServers")
		return ctrl.Result{}, err
	}

	// Track which ToolServers we process for cleanup later
	processedToolServers := make(map[string]bool)

	// Create or update resources for each ToolServer
	for _, toolServer := range toolServers {
		// Validate ToolServer
		if err := r.validateToolServer(toolServer); err != nil {
			log.Error(err, "Invalid ToolServer, skipping",
				"toolServerName", toolServer.Name,
				"toolServerNamespace", toolServer.Namespace)
			r.Recorder.Event(&toolGateway, "Warning", "InvalidToolServer",
				fmt.Sprintf("ToolServer %s/%s is invalid: %v", toolServer.Namespace, toolServer.Name, err))
			continue
		}

		if err := r.ensureToolServerResources(ctx, &toolGateway, toolServer); err != nil {
			log.Error(err, "Failed to ensure ToolServer resources",
				"toolServerName", toolServer.Name,
				"toolServerNamespace", toolServer.Namespace)
			r.Recorder.Event(&toolGateway, "Warning", "ToolServerResourcesFailed",
				fmt.Sprintf("Failed to create resources for ToolServer %s/%s: %v", toolServer.Namespace, toolServer.Name, err))
			// Continue processing other ToolServers
			continue
		}
		processedToolServers[fmt.Sprintf("%s/%s", toolServer.Namespace, toolServer.Name)] = true
	}

	// Cleanup stale resources
	if err := r.cleanupStaleResources(ctx, &toolGateway, processedToolServers); err != nil {
		log.Error(err, "Failed to cleanup stale resources")
		// Don't fail reconciliation for cleanup errors, just log
	}

	return ctrl.Result{}, nil
}

// shouldProcessToolGateway determines if this controller is responsible for the given ToolGateway
func (r *ToolGatewayReconciler) shouldProcessToolGateway(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) bool {
	log := logf.FromContext(ctx)

	// List all ToolGatewayClasses
	var toolGatewayClassList agentruntimev1alpha1.ToolGatewayClassList
	if err := r.List(ctx, &toolGatewayClassList); err != nil {
		log.Error(err, "Failed to list ToolGatewayClasses")
		r.Recorder.Event(toolGateway, "Warning", "ListFailed",
			fmt.Sprintf("Failed to list ToolGatewayClasses: %v", err))
		return false
	}

	// Filter to only classes managed by this controller
	var kgatewayClasses []agentruntimev1alpha1.ToolGatewayClass
	for _, tgc := range toolGatewayClassList.Items {
		if tgc.Spec.Controller == ToolGatewayKgatewayControllerName {
			kgatewayClasses = append(kgatewayClasses, tgc)
		}
	}

	// If className is explicitly set, check if it matches any of our managed classes
	toolGatewayClassName := toolGateway.Spec.ToolGatewayClassName
	if toolGatewayClassName != "" {
		for _, kgc := range kgatewayClasses {
			if kgc.Name == toolGatewayClassName {
				return true
			}
		}
	}

	// Look for ToolGatewayClass with default annotation among filtered classes
	for _, kgc := range kgatewayClasses {
		if kgc.Annotations["toolgatewayclass.kubernetes.io/is-default-class"] == "true" {
			return true
		}
	}

	return false
}

// getToolServers queries all ToolServer resources across all namespaces
func (r *ToolGatewayReconciler) getToolServers(ctx context.Context) ([]*agentruntimev1alpha1.ToolServer, error) {
	var toolServerList agentruntimev1alpha1.ToolServerList
	if err := r.List(ctx, &toolServerList); err != nil {
		return nil, err
	}

	toolServers := make([]*agentruntimev1alpha1.ToolServer, len(toolServerList.Items))
	for i := range toolServerList.Items {
		toolServers[i] = &toolServerList.Items[i]
	}

	return toolServers, nil
}

// validateToolServer validates a ToolServer resource
func (r *ToolGatewayReconciler) validateToolServer(toolServer *agentruntimev1alpha1.ToolServer) error {
	if toolServer.Spec.Port <= 0 || toolServer.Spec.Port > 65535 {
		return fmt.Errorf("invalid port number: %d, must be between 1 and 65535", toolServer.Spec.Port)
	}

	if toolServer.Name == "" || toolServer.Namespace == "" {
		return fmt.Errorf("toolServer name and namespace are required")
	}

	// Validate DNS-1123 subdomain format for name length
	if len(toolServer.Name) > 253 {
		return fmt.Errorf("toolServer name too long: %s (max 253 characters)", toolServer.Name)
	}

	return nil
}

// cleanupResources performs cleanup when a ToolGateway is deleted
func (r *ToolGatewayReconciler) cleanupResources(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) {
	log := logf.FromContext(ctx)
	log.Info("Cleaning up resources for ToolGateway")

	// HTTPRoutes and Gateway will be automatically deleted due to owner references
	// AgentgatewayBackends need manual cleanup as they don't have cross-namespace owner references

	// List all AgentgatewayBackends with our labels
	backendList := &unstructured.UnstructuredList{}
	backendList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agentgateway.dev",
		Version: "v1alpha1",
		Kind:    "AgentgatewayBackendList",
	})

	if err := r.List(ctx, backendList, client.MatchingLabels{
		toolGatewayLabel: toolGateway.Name,
	}); err != nil {
		// If CRD doesn't exist or other error, log but don't fail
		log.Error(err, "Failed to list AgentgatewayBackends for cleanup")
		return
	}

	// Delete each backend
	for _, backend := range backendList.Items {
		if err := r.Delete(ctx, &backend); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete AgentgatewayBackend",
				"name", backend.GetName(),
				"namespace", backend.GetNamespace())
			// Continue with other backends
		} else {
			log.Info("Deleted AgentgatewayBackend",
				"name", backend.GetName(),
				"namespace", backend.GetNamespace())
		}
	}
}

// cleanupStaleResources removes resources for ToolServers that no longer exist
func (r *ToolGatewayReconciler) cleanupStaleResources(
	ctx context.Context,
	toolGateway *agentruntimev1alpha1.ToolGateway,
	processedToolServers map[string]bool,
) error {
	log := logf.FromContext(ctx)

	// List HTTPRoutes owned by this ToolGateway
	var routeList gatewayv1.HTTPRouteList
	if err := r.List(ctx, &routeList, client.MatchingLabels{
		toolGatewayLabel: toolGateway.Name,
	}); err != nil {
		return fmt.Errorf("failed to list HTTPRoutes: %w", err)
	}

	// Delete routes for non-existent ToolServers
	for _, route := range routeList.Items {
		toolServerKey := route.Labels[toolServerNamespaceLabel] + "/" + route.Labels[toolServerLabel]
		if !processedToolServers[toolServerKey] {
			log.Info("Deleting stale HTTPRoute",
				"name", route.Name,
				"namespace", route.Namespace,
				"toolServer", toolServerKey)
			if err := r.Delete(ctx, &route); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to delete stale HTTPRoute",
					"name", route.Name,
					"namespace", route.Namespace)
			}
		}
	}

	// List AgentgatewayBackends with our labels
	backendList := &unstructured.UnstructuredList{}
	backendList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "agentgateway.dev",
		Version: "v1alpha1",
		Kind:    "AgentgatewayBackendList",
	})

	if err := r.List(ctx, backendList, client.MatchingLabels{
		toolGatewayLabel: toolGateway.Name,
	}); err != nil {
		log.Error(err, "Failed to list AgentgatewayBackends for cleanup")
		return nil // Don't fail reconciliation
	}

	// Delete backends for non-existent ToolServers
	for _, backend := range backendList.Items {
		toolServerKey := backend.GetLabels()[toolServerNamespaceLabel] + "/" + backend.GetLabels()[toolServerLabel]
		if !processedToolServers[toolServerKey] {
			log.Info("Deleting stale AgentgatewayBackend",
				"name", backend.GetName(),
				"namespace", backend.GetNamespace(),
				"toolServer", toolServerKey)
			if err := r.Delete(ctx, &backend); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to delete stale AgentgatewayBackend",
					"name", backend.GetName(),
					"namespace", backend.GetNamespace())
			}
		}
	}

	return nil
}

// ensureGateway creates or updates the Gateway for this ToolGateway
func (r *ToolGatewayReconciler) ensureGateway(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) error {
	log := logf.FromContext(ctx)

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      toolGateway.Name,
			Namespace: toolGateway.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, gateway, func() error {
		// Set owner reference
		if err := controllerutil.SetControllerReference(toolGateway, gateway, r.Scheme); err != nil {
			return err
		}

		// Set the gateway specification
		gateway.Spec = gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(agentGatewayClassName),
			Listeners: []gatewayv1.Listener{
				{
					Name:     gatewayv1.SectionName("http"),
					Protocol: gatewayv1.HTTPProtocolType,
					Port:     gatewayv1.PortNumber(80),
					AllowedRoutes: &gatewayv1.AllowedRoutes{
						Namespaces: &gatewayv1.RouteNamespaces{
							From: ptr.To(gatewayv1.NamespacesFromAll),
						},
					},
				},
			},
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update Gateway: %w", err)
	}

	log.Info("Gateway reconciled", "operation", op, "name", gateway.Name, "namespace", gateway.Namespace)

	// Record event for user visibility
	switch op {
	case controllerutil.OperationResultCreated:
		r.Recorder.Event(toolGateway, "Normal", "GatewayCreated",
			fmt.Sprintf("Created Gateway %s", gateway.Name))
	case controllerutil.OperationResultUpdated:
		r.Recorder.Event(toolGateway, "Normal", "GatewayUpdated",
			fmt.Sprintf("Updated Gateway %s", gateway.Name))
	}

	return nil
}

// ensureToolServerResources creates or updates resources for a ToolServer
func (r *ToolGatewayReconciler) ensureToolServerResources(
	ctx context.Context,
	toolGateway *agentruntimev1alpha1.ToolGateway,
	toolServer *agentruntimev1alpha1.ToolServer,
) error {
	// Ensure AgentgatewayBackend
	if err := r.ensureAgentgatewayBackend(ctx, toolGateway, toolServer); err != nil {
		return fmt.Errorf("failed to ensure AgentgatewayBackend: %w", err)
	}

	// Ensure HTTPRoute
	if err := r.ensureHTTPRoute(ctx, toolGateway, toolServer); err != nil {
		return fmt.Errorf("failed to ensure HTTPRoute: %w", err)
	}

	return nil
}

// ensureAgentgatewayBackend creates or updates an AgentgatewayBackend for a ToolServer
func (r *ToolGatewayReconciler) ensureAgentgatewayBackend(
	ctx context.Context,
	toolGateway *agentruntimev1alpha1.ToolGateway,
	toolServer *agentruntimev1alpha1.ToolServer,
) error {
	log := logf.FromContext(ctx)

	// Create AgentgatewayBackend as unstructured since CRD types are not yet available as Go module
	backend := &unstructured.Unstructured{}
	backend.SetAPIVersion("agentgateway.dev/v1alpha1")
	backend.SetKind("AgentgatewayBackend")
	backend.SetName(toolServer.Name)
	backend.SetNamespace(toolServer.Namespace)

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, backend, func() error {
		// Set labels for tracking and cleanup (no owner reference due to cross-namespace limitation)
		labels := backend.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[toolGatewayLabel] = toolGateway.Name
		labels[toolServerLabel] = toolServer.Name
		labels[toolServerNamespaceLabel] = toolServer.Namespace
		backend.SetLabels(labels)

		// Set the backend specification
		if err := unstructured.SetNestedMap(backend.Object, map[string]interface{}{
			"targets": []interface{}{
				map[string]interface{}{
					"name": "mcp-target",
					"static": map[string]interface{}{
						"host": fmt.Sprintf("%s.%s.svc.cluster.local",
							toolServer.Name, toolServer.Namespace),
						"port":     int64(toolServer.Spec.Port),
						"protocol": "StreamableHTTP",
					},
				},
			},
		}, "spec", "mcp"); err != nil {
			return fmt.Errorf("failed to set backend spec: %w", err)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update AgentgatewayBackend: %w", err)
	}

	log.Info("AgentgatewayBackend reconciled", "operation", op, "name", toolServer.Name, "namespace", toolServer.Namespace)

	// Record event
	if op == controllerutil.OperationResultCreated {
		r.Recorder.Event(toolGateway, "Normal", "BackendCreated",
			fmt.Sprintf("Created AgentgatewayBackend %s/%s for ToolServer", toolServer.Namespace, toolServer.Name))
	}

	return nil
}

// ensureHTTPRoute creates or updates an HTTPRoute for a ToolServer
func (r *ToolGatewayReconciler) ensureHTTPRoute(
	ctx context.Context,
	toolGateway *agentruntimev1alpha1.ToolGateway,
	toolServer *agentruntimev1alpha1.ToolServer,
) error {
	log := logf.FromContext(ctx)

	// Determine the path for this tool server
	path := toolServer.Spec.Path
	if path == "" {
		// Default path based on transport type
		if toolServer.Spec.TransportType == "sse" {
			path = "/sse"
		} else {
			path = "/mcp"
		}
	}

	// HTTPRoute must be in same namespace as ToolGateway to allow owner reference
	// Use a unique name combining gateway and toolserver
	routeName := fmt.Sprintf("%s-%s", toolGateway.Name, toolServer.Name)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      routeName,
			Namespace: toolGateway.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, route, func() error {
		// Set owner reference to the ToolGateway (now same namespace)
		if err := controllerutil.SetControllerReference(toolGateway, route, r.Scheme); err != nil {
			return err
		}

		// Set labels for tracking
		labels := route.Labels
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[toolGatewayLabel] = toolGateway.Name
		labels[toolServerLabel] = toolServer.Name
		labels[toolServerNamespaceLabel] = toolServer.Namespace
		route.Labels = labels

		// Set the route specification
		pathType := gatewayv1.PathMatchPathPrefix
		route.Spec = gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:      gatewayv1.ObjectName(toolGateway.Name),
						Namespace: ptr.To(gatewayv1.Namespace(toolGateway.Namespace)),
					},
				},
			},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Group:     ptr.To(gatewayv1.Group("agentgateway.dev")),
									Kind:      ptr.To(gatewayv1.Kind("AgentgatewayBackend")),
									Name:      gatewayv1.ObjectName(toolServer.Name),
									Namespace: ptr.To(gatewayv1.Namespace(toolServer.Namespace)),
								},
							},
						},
					},
					Matches: []gatewayv1.HTTPRouteMatch{
						{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  &pathType,
								Value: ptr.To(path),
							},
						},
					},
				},
			},
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update HTTPRoute: %w", err)
	}

	log.Info("HTTPRoute reconciled", "operation", op, "name", route.Name, "namespace", route.Namespace)

	// Record event
	if op == controllerutil.OperationResultCreated {
		r.Recorder.Event(toolGateway, "Normal", "RouteCreated",
			fmt.Sprintf("Created HTTPRoute %s for ToolServer %s/%s", route.Name, toolServer.Namespace, toolServer.Name))
	}

	return nil
}

// findToolGatewaysForToolServer returns all ToolGateway resources that need to be reconciled
// when a ToolServer changes. Since all gateways discover ToolServers across all namespaces,
// any ToolServer change affects all ToolGateway resources.
func (r *ToolGatewayReconciler) findToolGatewaysForToolServer(ctx context.Context, obj client.Object) []ctrl.Request {
	log := logf.FromContext(ctx)

	toolServer, ok := obj.(*agentruntimev1alpha1.ToolServer)
	if !ok {
		log.Error(nil, "Expected ToolServer object in watch handler")
		return []ctrl.Request{}
	}

	var toolGatewayList agentruntimev1alpha1.ToolGatewayList
	if err := r.List(ctx, &toolGatewayList); err != nil {
		log.Error(err, "Failed to list ToolGateways for ToolServer watch")
		return []ctrl.Request{}
	}

	// Filter to only gateways managed by this controller
	var requests []ctrl.Request
	for _, gw := range toolGatewayList.Items {
		if r.shouldProcessToolGateway(ctx, &gw) {
			requests = append(requests, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      gw.Name,
					Namespace: gw.Namespace,
				},
			})
		}
	}

	log.V(1).Info("Enqueuing ToolGateways for ToolServer change",
		"toolServer", fmt.Sprintf("%s/%s", toolServer.Namespace, toolServer.Name),
		"gatewayCount", len(requests))

	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *ToolGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentruntimev1alpha1.ToolGateway{}).
		Owns(&gatewayv1.Gateway{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Watches(
			&agentruntimev1alpha1.ToolServer{},
			handler.EnqueueRequestsFromMapFunc(r.findToolGatewaysForToolServer),
		).
		Named(ToolGatewayKgatewayControllerName).
		Complete(r)
}
