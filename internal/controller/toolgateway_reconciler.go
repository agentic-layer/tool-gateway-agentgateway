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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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

const ToolGatewayAgentgatewayControllerName = "runtime.agentic-layer.ai/tool-gateway-agentgateway-controller"

const (
	agentGatewayClassName = "agentgateway"
)

// ToolGatewayReconciler reconciles a ToolGateway object
type ToolGatewayReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgateways,verbs=get;list;watch;update;patch
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

	// Create or update the Gateway for this ToolGateway
	if err := r.ensureGateway(ctx, &toolGateway); err != nil {
		log.Error(err, "Failed to ensure Gateway")
		r.Recorder.Event(&toolGateway, "Warning", "GatewayFailed", err.Error())
		_ = r.updateStatus(ctx, &toolGateway, err)
		return ctrl.Result{}, err
	}

	// Create or update multiplex routes for MCP
	if err := r.ensureMultiplexRoutes(ctx, &toolGateway); err != nil {
		log.Error(err, "Failed to ensure multiplex routes")
		r.Recorder.Event(&toolGateway, "Warning", "MultiplexRoutesFailed", err.Error())
		_ = r.updateStatus(ctx, &toolGateway, err)
		return ctrl.Result{}, err
	}

	// Update ToolGateway status with cluster-local URL and Ready condition
	if err := r.updateStatus(ctx, &toolGateway, nil); err != nil {
		log.Error(err, "Failed to update ToolGateway status")
		return ctrl.Result{}, err
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
	var agentgatewayClasses []agentruntimev1alpha1.ToolGatewayClass
	for _, tgc := range toolGatewayClassList.Items {
		if tgc.Spec.Controller == ToolGatewayAgentgatewayControllerName {
			agentgatewayClasses = append(agentgatewayClasses, tgc)
		}
	}

	// If className is explicitly set, check if it matches any of our managed classes
	toolGatewayClassName := toolGateway.Spec.ToolGatewayClassName
	if toolGatewayClassName != "" {
		for _, agc := range agentgatewayClasses {
			if agc.Name == toolGatewayClassName {
				return true
			}
		}
	}

	// Look for ToolGatewayClass with default annotation among filtered classes
	for _, agc := range agentgatewayClasses {
		if agc.Annotations["toolgatewayclass.kubernetes.io/is-default-class"] == "true" {
			return true
		}
	}

	return false
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

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, gateway, func() error {
		// Set owner reference
		if err := controllerutil.SetControllerReference(toolGateway, gateway, r.Scheme); err != nil {
			return err
		}

		// Set the gateway specification
		gateway.Spec = gatewayv1.GatewaySpec{
			GatewayClassName: agentGatewayClassName,
			Listeners: []gatewayv1.Listener{
				{
					Name:     "http",
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

// updateStatus patches the ToolGateway status with the cluster-local URL and a Ready condition.
// reconcileErr is non-nil when called after a reconciliation failure.
func (r *ToolGatewayReconciler) updateStatus(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway, reconcileErr error) error {
	patch := client.MergeFrom(toolGateway.DeepCopy())

	if reconcileErr == nil {
		toolGateway.Status.Url = fmt.Sprintf("http://%s.%s.svc.cluster.local", toolGateway.Name, toolGateway.Namespace)
		apimeta.SetStatusCondition(&toolGateway.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "GatewayProgrammed",
			Message:            "Gateway has been created and configured",
			ObservedGeneration: toolGateway.Generation,
		})
	} else {
		apimeta.SetStatusCondition(&toolGateway.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "GatewayFailed",
			Message:            reconcileErr.Error(),
			ObservedGeneration: toolGateway.Generation,
		})
	}

	return r.Status().Patch(ctx, toolGateway, patch)
}

// ensureMultiplexRoutes creates multiplex MCP routes for the ToolGateway
func (r *ToolGatewayReconciler) ensureMultiplexRoutes(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) error {
	log := logf.FromContext(ctx)

	// Get all ToolServers that reference this ToolGateway
	toolServers, err := r.getToolServersForGateway(ctx, toolGateway)
	if err != nil {
		return fmt.Errorf("failed to get ToolServers for gateway: %w", err)
	}

	if len(toolServers) == 0 {
		log.Info("No ToolServers found for gateway, skipping multiplex routes")
		return nil
	}

	// Create namespace-level multiplex backend and route (/<namespace>/mcp)
	if err := r.ensureNamespaceMultiplexBackend(ctx, toolGateway, toolServers); err != nil {
		return fmt.Errorf("failed to ensure namespace multiplex backend: %w", err)
	}

	if err := r.ensureNamespaceMultiplexRoute(ctx, toolGateway); err != nil {
		return fmt.Errorf("failed to ensure namespace multiplex route: %w", err)
	}

	// Create root-level multiplex backend and route (/mcp)
	if err := r.ensureRootMultiplexBackend(ctx, toolGateway, toolServers); err != nil {
		return fmt.Errorf("failed to ensure root multiplex backend: %w", err)
	}

	if err := r.ensureRootMultiplexRoute(ctx, toolGateway); err != nil {
		return fmt.Errorf("failed to ensure root multiplex route: %w", err)
	}

	return nil
}

// getToolServersForGateway retrieves all ToolServers that reference this ToolGateway
func (r *ToolGatewayReconciler) getToolServersForGateway(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) ([]agentruntimev1alpha1.ToolServer, error) {
	var toolServerList agentruntimev1alpha1.ToolServerList
	if err := r.List(ctx, &toolServerList); err != nil {
		return nil, fmt.Errorf("failed to list ToolServers: %w", err)
	}

	var result []agentruntimev1alpha1.ToolServer
	for _, ts := range toolServerList.Items {
		// Check if ToolServer has a ToolGatewayRef in status pointing to this gateway
		if ts.Status.ToolGatewayRef != nil &&
			ts.Status.ToolGatewayRef.Name == toolGateway.Name &&
			ts.Status.ToolGatewayRef.Namespace == toolGateway.Namespace {
			result = append(result, ts)
		}
	}

	return result, nil
}

// ensureNamespaceMultiplexBackend creates a multiplex backend for all toolservers in the gateway's namespace
func (r *ToolGatewayReconciler) ensureNamespaceMultiplexBackend(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway, toolServers []agentruntimev1alpha1.ToolServer) error {
	log := logf.FromContext(ctx)

	// Filter ToolServers to only those in the gateway's namespace
	var namespacedServers []agentruntimev1alpha1.ToolServer
	for _, ts := range toolServers {
		if ts.Namespace == toolGateway.Namespace {
			namespacedServers = append(namespacedServers, ts)
		}
	}

	if len(namespacedServers) == 0 {
		log.Info("No ToolServers in gateway namespace, skipping namespace multiplex backend")
		return nil
	}

	backend := &unstructured.Unstructured{}
	backend.SetAPIVersion("agentgateway.dev/v1alpha1")
	backend.SetKind("AgentgatewayBackend")
	backend.SetName(fmt.Sprintf("%s-multiplex-ns", toolGateway.Name))
	backend.SetNamespace(toolGateway.Namespace)

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, backend, func() error {
		// Set owner reference to ToolGateway for automatic cleanup
		if err := controllerutil.SetControllerReference(toolGateway, backend, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}

		// Build targets list for all toolservers in namespace
		targets := make([]interface{}, 0, len(namespacedServers))
		for _, ts := range namespacedServers {
			targets = append(targets, map[string]interface{}{
				"name": ts.Name,
				"static": map[string]interface{}{
					"host":     fmt.Sprintf("%s.%s.svc.cluster.local", ts.Name, ts.Namespace),
					"port":     int64(ts.Spec.Port),
					"protocol": "StreamableHTTP",
				},
			})
		}

		// Set the backend specification
		if err := unstructured.SetNestedMap(backend.Object, map[string]interface{}{
			"targets": targets,
		}, "spec", "mcp"); err != nil {
			return fmt.Errorf("failed to set backend spec: %w", err)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update namespace multiplex backend: %w", err)
	}

	log.Info("Namespace multiplex backend reconciled", "operation", op, "name", backend.GetName())

	switch op {
	case controllerutil.OperationResultCreated:
		r.Recorder.Event(toolGateway, "Normal", "MultiplexBackendCreated",
			fmt.Sprintf("Created namespace multiplex backend %s", backend.GetName()))
	case controllerutil.OperationResultUpdated:
		r.Recorder.Event(toolGateway, "Normal", "MultiplexBackendUpdated",
			fmt.Sprintf("Updated namespace multiplex backend %s", backend.GetName()))
	}

	return nil
}

// ensureNamespaceMultiplexRoute creates HTTPRoute for /<namespace>/mcp
func (r *ToolGatewayReconciler) ensureNamespaceMultiplexRoute(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) error {
	log := logf.FromContext(ctx)

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-multiplex-ns", toolGateway.Name),
			Namespace: toolGateway.Namespace,
		},
	}

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, route, func() error {
		// Set owner reference to ToolGateway for automatic cleanup
		if err := controllerutil.SetControllerReference(toolGateway, route, r.Scheme); err != nil {
			return err
		}

		pathType := gatewayv1.PathMatchPathPrefix
		path := fmt.Sprintf("/%s/mcp", toolGateway.Namespace)

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
									Name:      gatewayv1.ObjectName(fmt.Sprintf("%s-multiplex-ns", toolGateway.Name)),
									Namespace: ptr.To(gatewayv1.Namespace(toolGateway.Namespace)),
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
		return fmt.Errorf("failed to create or update namespace multiplex route: %w", err)
	}

	log.Info("Namespace multiplex route reconciled", "operation", op, "name", route.Name)

	switch op {
	case controllerutil.OperationResultCreated:
		r.Recorder.Event(toolGateway, "Normal", "MultiplexRouteCreated",
			fmt.Sprintf("Created namespace multiplex route %s", route.Name))
	case controllerutil.OperationResultUpdated:
		r.Recorder.Event(toolGateway, "Normal", "MultiplexRouteUpdated",
			fmt.Sprintf("Updated namespace multiplex route %s", route.Name))
	}

	return nil
}

// ensureRootMultiplexBackend creates a multiplex backend for all toolservers
func (r *ToolGatewayReconciler) ensureRootMultiplexBackend(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway, toolServers []agentruntimev1alpha1.ToolServer) error {
	log := logf.FromContext(ctx)

	backend := &unstructured.Unstructured{}
	backend.SetAPIVersion("agentgateway.dev/v1alpha1")
	backend.SetKind("AgentgatewayBackend")
	backend.SetName(fmt.Sprintf("%s-multiplex-root", toolGateway.Name))
	backend.SetNamespace(toolGateway.Namespace)

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, backend, func() error {
		// Set owner reference to ToolGateway for automatic cleanup
		if err := controllerutil.SetControllerReference(toolGateway, backend, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}

		// Build targets list for all toolservers
		targets := make([]interface{}, 0, len(toolServers))
		for _, ts := range toolServers {
			targets = append(targets, map[string]interface{}{
				"name": fmt.Sprintf("%s-%s", ts.Namespace, ts.Name),
				"static": map[string]interface{}{
					"host":     fmt.Sprintf("%s.%s.svc.cluster.local", ts.Name, ts.Namespace),
					"port":     int64(ts.Spec.Port),
					"protocol": "StreamableHTTP",
				},
			})
		}

		// Set the backend specification
		if err := unstructured.SetNestedMap(backend.Object, map[string]interface{}{
			"targets": targets,
		}, "spec", "mcp"); err != nil {
			return fmt.Errorf("failed to set backend spec: %w", err)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update root multiplex backend: %w", err)
	}

	log.Info("Root multiplex backend reconciled", "operation", op, "name", backend.GetName())

	switch op {
	case controllerutil.OperationResultCreated:
		r.Recorder.Event(toolGateway, "Normal", "MultiplexBackendCreated",
			fmt.Sprintf("Created root multiplex backend %s", backend.GetName()))
	case controllerutil.OperationResultUpdated:
		r.Recorder.Event(toolGateway, "Normal", "MultiplexBackendUpdated",
			fmt.Sprintf("Updated root multiplex backend %s", backend.GetName()))
	}

	return nil
}

// ensureRootMultiplexRoute creates HTTPRoute for /mcp
func (r *ToolGatewayReconciler) ensureRootMultiplexRoute(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) error {
	log := logf.FromContext(ctx)

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-multiplex-root", toolGateway.Name),
			Namespace: toolGateway.Namespace,
		},
	}

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, route, func() error {
		// Set owner reference to ToolGateway for automatic cleanup
		if err := controllerutil.SetControllerReference(toolGateway, route, r.Scheme); err != nil {
			return err
		}

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
									Name:      gatewayv1.ObjectName(fmt.Sprintf("%s-multiplex-root", toolGateway.Name)),
									Namespace: ptr.To(gatewayv1.Namespace(toolGateway.Namespace)),
								},
							},
						},
					},
					Matches: []gatewayv1.HTTPRouteMatch{
						{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  &pathType,
								Value: ptr.To("/mcp"),
							},
						},
					},
				},
			},
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update root multiplex route: %w", err)
	}

	log.Info("Root multiplex route reconciled", "operation", op, "name", route.Name)

	switch op {
	case controllerutil.OperationResultCreated:
		r.Recorder.Event(toolGateway, "Normal", "MultiplexRouteCreated",
			fmt.Sprintf("Created root multiplex route %s", route.Name))
	case controllerutil.OperationResultUpdated:
		r.Recorder.Event(toolGateway, "Normal", "MultiplexRouteUpdated",
			fmt.Sprintf("Updated root multiplex route %s", route.Name))
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ToolGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentruntimev1alpha1.ToolGateway{}).
		Owns(&gatewayv1.Gateway{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Watches(
			&agentruntimev1alpha1.ToolServer{},
			handler.EnqueueRequestsFromMapFunc(r.findToolGatewayForToolServer),
		).
		Named(ToolGatewayAgentgatewayControllerName).
		Complete(r)
}

// findToolGatewayForToolServer maps a ToolServer to its associated ToolGateway for reconciliation
func (r *ToolGatewayReconciler) findToolGatewayForToolServer(ctx context.Context, obj client.Object) []ctrl.Request {
	toolServer, ok := obj.(*agentruntimev1alpha1.ToolServer)
	if !ok {
		return nil
	}

	// Check if ToolServer has a ToolGatewayRef in status
	if toolServer.Status.ToolGatewayRef == nil {
		return nil
	}

	// Trigger reconciliation for the referenced ToolGateway
	return []ctrl.Request{
		{
			NamespacedName: client.ObjectKey{
				Name:      toolServer.Status.ToolGatewayRef.Name,
				Namespace: toolServer.Status.ToolGatewayRef.Namespace,
			},
		},
	}
}
