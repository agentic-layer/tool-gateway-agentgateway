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
	"k8s.io/apimachinery/pkg/runtime"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

const ToolServerAgentgatewayControllerName = "runtime.agentic-layer.ai/toolserver-agentgateway-controller"

// ToolServerReconciler reconciles a ToolServer object
type ToolServerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder kevents.EventRecorder
}

// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolservers,verbs=get;list;watch
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

	// Read ToolGateway reference from status (set by agent-runtime-operator)
	gatewayRef := toolServer.Status.ToolGatewayRef
	if gatewayRef == nil {
		log.Info("No ToolGatewayRef in status yet, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Create or update AgentgatewayBackend
	if err := r.ensureAgentgatewayBackend(ctx, &toolServer); err != nil {
		log.Error(err, "Failed to ensure AgentgatewayBackend")
		r.Recorder.Eventf(&toolServer, nil, "Warning", "BackendFailed", "BackendFailed", "%s", err.Error())
		return ctrl.Result{}, err
	}

	// Create or update HTTPRoute
	if err := r.ensureHTTPRoute(ctx, &toolServer, gatewayRef); err != nil {
		log.Error(err, "Failed to ensure HTTPRoute")
		r.Recorder.Eventf(&toolServer, nil, "Warning", "RouteFailed", "RouteFailed", "%s", err.Error())
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// ensureAgentgatewayBackend creates or updates an AgentgatewayBackend for a ToolServer
func (r *ToolServerReconciler) ensureAgentgatewayBackend(
	ctx context.Context,
	toolServer *agentruntimev1alpha1.ToolServer,
) error {
	log := logf.FromContext(ctx)

	// Create AgentgatewayBackend as unstructured since CRD types are not yet available as Go module
	backend := newAgentgatewayBackend(toolServer.Status.ToolGatewayRef.Name+"-"+toolServer.Name, toolServer.Namespace)

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, backend, func() error {
		// Set owner reference to ToolServer for automatic cleanup
		if err := controllerutil.SetControllerReference(toolServer, backend, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}

		target := buildMCPTarget("mcp-target", toolServerHost(toolServer.Name, toolServer.Namespace), toolServer.Spec.Port)
		if err := setMCPTargets(backend, []interface{}{target}); err != nil {
			return fmt.Errorf("failed to set backend spec: %w", err)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update AgentgatewayBackend: %w", err)
	}

	log.Info("AgentgatewayBackend reconciled", "operation", op, "name", backend.GetName(), "namespace", toolServer.Namespace)

	// Record event
	switch op {
	case controllerutil.OperationResultCreated:
		r.Recorder.Eventf(toolServer, nil, "Normal", "BackendCreated", "BackendCreated",
			"Created AgentgatewayBackend %s/%s", toolServer.Namespace, toolServer.Name)
	case controllerutil.OperationResultUpdated:
		r.Recorder.Eventf(toolServer, nil, "Normal", "BackendUpdated", "BackendUpdated",
			"Updated AgentgatewayBackend %s/%s", toolServer.Namespace, toolServer.Name)
	}

	return nil
}

// ensureHTTPRoute creates or updates an HTTPRoute for a ToolServer
func (r *ToolServerReconciler) ensureHTTPRoute(
	ctx context.Context,
	toolServer *agentruntimev1alpha1.ToolServer,
	gatewayRef *corev1.ObjectReference,
) error {
	log := logf.FromContext(ctx)

	// Compute unique path for this ToolServer
	suffix := "/mcp"
	if toolServer.Spec.TransportType == "sse" {
		suffix = "/sse"
	}
	path := fmt.Sprintf("/%s/%s%s", toolServer.Namespace, toolServer.Name, suffix)

	// HTTPRoute in same namespace as ToolServer, owned by ToolServer
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gatewayRef.Name + "-" + toolServer.Name,
			Namespace: toolServer.Namespace,
		},
	}

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, route, func() error {
		// Set owner reference to the ToolServer for automatic cleanup
		if err := controllerutil.SetControllerReference(toolServer, route, r.Scheme); err != nil {
			return err
		}

		// Set the route specification
		route.Spec = buildHTTPRouteSpec(
			gatewayRef.Name, gatewayRef.Namespace,
			gatewayRef.Name+"-"+toolServer.Name, toolServer.Namespace,
			path,
		)
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update HTTPRoute: %w", err)
	}

	log.Info("HTTPRoute reconciled", "operation", op, "name", route.Name, "namespace", route.Namespace)

	// Record event
	switch op {
	case controllerutil.OperationResultCreated:
		r.Recorder.Eventf(toolServer, nil, "Normal", "RouteCreated", "RouteCreated",
			"Created HTTPRoute %s for Gateway %s/%s", route.Name, gatewayRef.Namespace, gatewayRef.Name)
	case controllerutil.OperationResultUpdated:
		r.Recorder.Eventf(toolServer, nil, "Normal", "RouteUpdated", "RouteUpdated",
			"Updated HTTPRoute %s for Gateway %s/%s", route.Name, gatewayRef.Namespace, gatewayRef.Name)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ToolServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentruntimev1alpha1.ToolServer{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Named(ToolServerAgentgatewayControllerName).
		Complete(r)
}
