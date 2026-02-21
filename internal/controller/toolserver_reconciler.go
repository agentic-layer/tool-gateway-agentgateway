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

	corev1 "k8s.io/api/core/v1"
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

	// Find the ToolGateway to use
	toolGateway, err := r.findToolGateway(ctx, &toolServer)
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

	// Compute unique path for this ToolServer
	suffix := "/mcp"
	if toolServer.Spec.TransportType == "sse" {
		suffix = "/sse"
	}
	path := fmt.Sprintf("/%s/%s%s", toolServer.Namespace, toolServer.Name, suffix)

	// Create or update HTTPRoute
	if err := r.ensureHTTPRoute(ctx, &toolServer, toolGateway, path); err != nil {
		log.Error(err, "Failed to ensure HTTPRoute")
		r.Recorder.Event(&toolServer, "Warning", "RouteFailed", err.Error())
		return ctrl.Result{}, err
	}

	// Update ToolServer status with gateway URL
	if err := r.updateStatus(ctx, &toolServer, toolGateway, path); err != nil {
		log.Error(err, "Failed to update ToolServer status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// findToolGateway finds a ToolGateway to use for this ToolServer
// It uses the ToolGatewayRef from the ToolServer spec if specified,
// otherwise tries to find a default ToolGateway in the cluster
func (r *ToolServerReconciler) findToolGateway(ctx context.Context, toolServer *agentruntimev1alpha1.ToolServer) (*agentruntimev1alpha1.ToolGateway, error) {
	log := logf.FromContext(ctx)

	// If ToolGatewayRef is specified, use it
	if toolServer.Spec.ToolGatewayRef != nil {
		ref := toolServer.Spec.ToolGatewayRef

		// Default namespace to ToolServer's namespace if not specified
		namespace := ref.Namespace
		if namespace == "" {
			namespace = toolServer.Namespace
		}

		toolGateway := &agentruntimev1alpha1.ToolGateway{}
		if err := r.Get(ctx, client.ObjectKey{
			Name:      ref.Name,
			Namespace: namespace,
		}, toolGateway); err != nil {
			return nil, fmt.Errorf("failed to get referenced ToolGateway %s/%s: %w", namespace, ref.Name, err)
		}

		log.Info("Using referenced ToolGateway", "name", toolGateway.Name, "namespace", toolGateway.Namespace)
		return toolGateway, nil
	}

	// If no ToolGatewayRef specified, try to find a default ToolGateway
	var toolGatewayList agentruntimev1alpha1.ToolGatewayList
	if err := r.List(ctx, &toolGatewayList); err != nil {
		return nil, fmt.Errorf("failed to list ToolGateways: %w", err)
	}

	if len(toolGatewayList.Items) == 0 {
		log.Info("No ToolGateways found")
		return nil, nil
	}

	// Use the first one as default
	log.Info("Using default ToolGateway", "name", toolGatewayList.Items[0].Name, "namespace", toolGatewayList.Items[0].Namespace)
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
	path string,
) error {
	log := logf.FromContext(ctx)

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

// updateStatus updates the ToolServer status with the internal gateway URL
func (r *ToolServerReconciler) updateStatus(
	ctx context.Context,
	toolServer *agentruntimev1alpha1.ToolServer,
	toolGateway *agentruntimev1alpha1.ToolGateway,
	path string,
) error {
	toolServer.Status.Url = fmt.Sprintf("http://%s.%s.svc.cluster.local%s",
		toolGateway.Name, toolGateway.Namespace, path)
	toolServer.Status.ToolGatewayRef = &corev1.ObjectReference{
		Name:      toolGateway.Name,
		Namespace: toolGateway.Namespace,
	}
	return r.Status().Update(ctx, toolServer)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ToolServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentruntimev1alpha1.ToolServer{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Named(ToolServerKgatewayControllerName).
		Complete(r)
}
