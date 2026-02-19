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
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

const ToolServerKgatewayControllerName = "runtime.agentic-layer.ai/toolserver-kgateway-controller"

// ToolServerReconciler reconciles a ToolServer object
type ToolServerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaybackends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ToolServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the ToolServer instance
	var toolServer agentruntimev1alpha1.ToolServer
	if err := r.Get(ctx, req.NamespacedName, &toolServer); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ToolServer resource not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get ToolServer")
		return ctrl.Result{}, err
	}

	log.Info("Reconciling ToolServer",
		"name", toolServer.Name,
		"namespace", toolServer.Namespace)

	// Validate ToolServer
	if err := r.validateToolServer(&toolServer); err != nil {
		log.Error(err, "Invalid ToolServer")
		r.Recorder.Event(&toolServer, "Warning", "ValidationFailed", err.Error())
		return ctrl.Result{}, nil // Don't requeue for validation errors
	}

	// Find the ToolGateway to use
	toolGateway, err := r.findToolGateway(ctx)
	if err != nil {
		log.Error(err, "Failed to find ToolGateway")
		return ctrl.Result{}, err
	}
	if toolGateway == nil {
		log.Info("No ToolGateway found, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Create or update AgentgatewayBackend
	if err := r.ensureAgentgatewayBackend(ctx, &toolServer); err != nil {
		log.Error(err, "Failed to ensure AgentgatewayBackend")
		r.Recorder.Event(&toolServer, "Warning", "BackendFailed", err.Error())
		return ctrl.Result{}, err
	}

	// Create or update HTTPRoute
	if err := r.ensureHTTPRoute(ctx, &toolServer, toolGateway); err != nil {
		log.Error(err, "Failed to ensure HTTPRoute")
		r.Recorder.Event(&toolServer, "Warning", "RouteFailed", err.Error())
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// validateToolServer validates a ToolServer resource
func (r *ToolServerReconciler) validateToolServer(toolServer *agentruntimev1alpha1.ToolServer) error {
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

// findToolGateway finds a ToolGateway to use for this ToolServer
// For now, we assume there is only one ToolGateway and use it
func (r *ToolServerReconciler) findToolGateway(ctx context.Context) (*agentruntimev1alpha1.ToolGateway, error) {
	log := logf.FromContext(ctx)

	var toolGatewayList agentruntimev1alpha1.ToolGatewayList
	if err := r.List(ctx, &toolGatewayList); err != nil {
		return nil, fmt.Errorf("failed to list ToolGateways: %w", err)
	}

	if len(toolGatewayList.Items) == 0 {
		log.Info("No ToolGateways found")
		return nil, nil
	}

	// For now, just use the first one
	// TODO: Allow ToolServer to specify which ToolGateway to use
	return &toolGatewayList.Items[0], nil
}

// ensureAgentgatewayBackend creates or updates an AgentgatewayBackend for a ToolServer
func (r *ToolServerReconciler) ensureAgentgatewayBackend(
	ctx context.Context,
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
		// Set owner reference to ToolServer for automatic cleanup
		if err := controllerutil.SetControllerReference(toolServer, backend, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}

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
	switch op {
	case controllerutil.OperationResultCreated:
		r.Recorder.Event(toolServer, "Normal", "BackendCreated",
			fmt.Sprintf("Created AgentgatewayBackend %s/%s", toolServer.Namespace, toolServer.Name))
	case controllerutil.OperationResultUpdated:
		r.Recorder.Event(toolServer, "Normal", "BackendUpdated",
			fmt.Sprintf("Updated AgentgatewayBackend %s/%s", toolServer.Namespace, toolServer.Name))
	}

	return nil
}

// ensureHTTPRoute creates or updates an HTTPRoute for a ToolServer
func (r *ToolServerReconciler) ensureHTTPRoute(
	ctx context.Context,
	toolServer *agentruntimev1alpha1.ToolServer,
	toolGateway *agentruntimev1alpha1.ToolGateway,
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

	// HTTPRoute in same namespace as ToolServer, owned by ToolServer
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      toolServer.Name,
			Namespace: toolServer.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, route, func() error {
		// Set owner reference to the ToolServer for automatic cleanup
		if err := controllerutil.SetControllerReference(toolServer, route, r.Scheme); err != nil {
			return err
		}

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
	switch op {
	case controllerutil.OperationResultCreated:
		r.Recorder.Event(toolServer, "Normal", "RouteCreated",
			fmt.Sprintf("Created HTTPRoute %s for Gateway %s/%s", route.Name, toolGateway.Namespace, toolGateway.Name))
	case controllerutil.OperationResultUpdated:
		r.Recorder.Event(toolServer, "Normal", "RouteUpdated",
			fmt.Sprintf("Updated HTTPRoute %s for Gateway %s/%s", route.Name, toolGateway.Namespace, toolGateway.Name))
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ToolServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentruntimev1alpha1.ToolServer{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Named(ToolServerKgatewayControllerName).
		Complete(r)
}
