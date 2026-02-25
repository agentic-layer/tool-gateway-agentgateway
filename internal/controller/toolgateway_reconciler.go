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
	"k8s.io/apimachinery/pkg/runtime"
	kevents "k8s.io/client-go/tools/events"
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
	Recorder kevents.EventRecorder
}

// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgateways,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaybackends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewayparameters,verbs=get;list;watch;create;update;patch;delete
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

	if err := r.ensureAgentgatewayParameters(ctx, &toolGateway); err != nil {
		log.Error(err, "Failed to ensure AgentgatewayParameters")
		r.Recorder.Eventf(&toolGateway, nil, "Warning", "AgentgatewayParametersFailed", "AgentgatewayParametersFailed", "%s", err.Error())
		_ = r.updateStatus(ctx, &toolGateway, err)
		return ctrl.Result{}, err
	}

	// Create or update the Gateway for this ToolGateway
	if err := r.ensureGateway(ctx, &toolGateway); err != nil {
		log.Error(err, "Failed to ensure Gateway")
		r.Recorder.Eventf(&toolGateway, nil, "Warning", "GatewayFailed", "GatewayFailed", "%s", err.Error())
		_ = r.updateStatus(ctx, &toolGateway, err)
		return ctrl.Result{}, err
	}

	// Create or update multiplex routes for MCP
	if err := r.ensureMultiplexRoutes(ctx, &toolGateway); err != nil {
		log.Error(err, "Failed to ensure multiplex routes")
		r.Recorder.Eventf(&toolGateway, nil, "Warning", "MultiplexRoutesFailed", "MultiplexRoutesFailed", "%s", err.Error())
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
		r.Recorder.Eventf(toolGateway, nil, "Warning", "ListFailed", "ListFailed",
			"Failed to list ToolGatewayClasses: %v", err)
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

		gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
			ParametersRef: &gatewayv1.LocalParametersReference{
				Group: "agentgateway.dev",
				Kind:  "AgentgatewayParameters",
				Name:  toolGateway.Name,
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
		r.Recorder.Eventf(toolGateway, nil, "Normal", "GatewayCreated", "GatewayCreated",
			"Created Gateway %s", gateway.Name)
	case controllerutil.OperationResultUpdated:
		r.Recorder.Eventf(toolGateway, nil, "Normal", "GatewayUpdated", "GatewayUpdated",
			"Updated Gateway %s", gateway.Name)
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

// ensureAgentgatewayParameters creates or updates an AgentgatewayParameters resource when Environment is configured
func (r *ToolGatewayReconciler) ensureAgentgatewayParameters(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) error {
	log := logf.FromContext(ctx)

	params := newAgentgatewayParameters(toolGateway.Name, toolGateway.Namespace)

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, params, func() error {
		if err := controllerutil.SetControllerReference(toolGateway, params, r.Scheme); err != nil {
			return err
		}

		return setAgentgatewayParametersSpec(params, toolGateway.Spec)
	})

	if err != nil {
		return fmt.Errorf("failed to create or update AgentgatewayParameters: %w", err)
	}

	log.Info("AgentgatewayParameters reconciled", "operation", op, "name", params.GetName())

	switch op {
	case controllerutil.OperationResultCreated:
		r.Recorder.Eventf(toolGateway, nil, "Normal", "AgentgatewayParametersCreated", "AgentgatewayParametersCreated",
			"Created AgentgatewayParameters %s", params.GetName())
	case controllerutil.OperationResultUpdated:
		r.Recorder.Eventf(toolGateway, nil, "Normal", "AgentgatewayParametersUpdated", "AgentgatewayParametersUpdated",
			"Updated AgentgatewayParameters %s", params.GetName())
	}

	return nil
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

	// Group ToolServers by their namespace and create one backend+route per namespace
	byNamespace := map[string][]agentruntimev1alpha1.ToolServer{}
	for _, ts := range toolServers {
		byNamespace[ts.Namespace] = append(byNamespace[ts.Namespace], ts)
	}
	for ns, servers := range byNamespace {
		name := toolGateway.Name + "-" + ns
		if err := r.ensureMultiplexBackend(ctx, toolGateway, servers, name, ns); err != nil {
			return fmt.Errorf("failed to ensure namespace multiplex backend for %s: %w", ns, err)
		}
		if err := r.ensureMultiplexRoute(ctx, toolGateway, name, ns, fmt.Sprintf("/%s/mcp", ns)); err != nil {
			return fmt.Errorf("failed to ensure namespace multiplex route for %s: %w", ns, err)
		}
	}

	// Create root-level multiplex backend and route (/mcp)
	if err := r.ensureMultiplexBackend(ctx, toolGateway, toolServers, toolGateway.Name, toolGateway.Namespace); err != nil {
		return fmt.Errorf("failed to ensure root multiplex backend: %w", err)
	}

	if err := r.ensureMultiplexRoute(ctx, toolGateway, toolGateway.Name, toolGateway.Namespace, "/mcp"); err != nil {
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

// ensureMultiplexBackend creates a multiplex backend for the given ToolServers.
// name and namespace specify where the AgentgatewayBackend is created.
// Owner reference is only set when the backend is in the same namespace as the ToolGateway,
// since Kubernetes does not support cross-namespace owner references.
func (r *ToolGatewayReconciler) ensureMultiplexBackend(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway, toolServers []agentruntimev1alpha1.ToolServer, name, namespace string) error {
	log := logf.FromContext(ctx)

	backend := newAgentgatewayBackend(name, namespace)

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, backend, func() error {
		// Owner reference enables automatic cleanup when the ToolGateway is deleted,
		// but only within the same namespace (cross-namespace owner refs are not supported).
		if namespace == toolGateway.Namespace {
			if err := controllerutil.SetControllerReference(toolGateway, backend, r.Scheme); err != nil {
				return fmt.Errorf("failed to set owner reference: %w", err)
			}
		}

		// Build targets list for all ToolServers
		// Always include namespace in target name for uniqueness across namespaces
		targets := make([]interface{}, 0, len(toolServers))
		for _, ts := range toolServers {
			targets = append(targets, buildMCPTarget(
				fmt.Sprintf("%s-%s", ts.Namespace, ts.Name),
				toolServerHost(ts.Name, ts.Namespace),
				ts.Spec.Port,
				ts.Spec.Path,
			))
		}

		if err := setMCPTargets(backend, targets); err != nil {
			return fmt.Errorf("failed to set backend spec: %w", err)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update multiplex backend: %w", err)
	}

	log.Info("Multiplex backend reconciled", "operation", op, "name", backend.GetName(), "namespace", namespace)

	switch op {
	case controllerutil.OperationResultCreated:
		r.Recorder.Eventf(toolGateway, nil, "Normal", "MultiplexBackendCreated", "MultiplexBackendCreated",
			"Created multiplex backend %s", backend.GetName())
	case controllerutil.OperationResultUpdated:
		r.Recorder.Eventf(toolGateway, nil, "Normal", "MultiplexBackendUpdated", "MultiplexBackendUpdated",
			"Updated multiplex backend %s", backend.GetName())
	}

	return nil
}

// ensureMultiplexRoute creates an HTTPRoute for the given path.
// name and namespace specify where the HTTPRoute is created.
// The co-located AgentgatewayBackend is referenced by the same name and namespace.
// Owner reference is only set when the route is in the same namespace as the ToolGateway.
func (r *ToolGatewayReconciler) ensureMultiplexRoute(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway, name, namespace, path string) error {
	log := logf.FromContext(ctx)

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, route, func() error {
		// Owner reference enables automatic cleanup when the ToolGateway is deleted,
		// but only within the same namespace (cross-namespace owner refs are not supported).
		if namespace == toolGateway.Namespace {
			if err := controllerutil.SetControllerReference(toolGateway, route, r.Scheme); err != nil {
				return err
			}
		}

		route.Spec = buildHTTPRouteSpec(toolGateway.Name, toolGateway.Namespace, name, namespace, path)
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to create or update multiplex route: %w", err)
	}

	log.Info("Multiplex route reconciled", "operation", op, "name", route.Name, "namespace", namespace, "path", path)

	switch op {
	case controllerutil.OperationResultCreated:
		r.Recorder.Eventf(toolGateway, nil, "Normal", "MultiplexRouteCreated", "MultiplexRouteCreated",
			"Created multiplex route %s", route.Name)
	case controllerutil.OperationResultUpdated:
		r.Recorder.Eventf(toolGateway, nil, "Normal", "MultiplexRouteUpdated", "MultiplexRouteUpdated",
			"Updated multiplex route %s", route.Name)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ToolGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	agentgatewayParams := newAgentgatewayParameters("", "")
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentruntimev1alpha1.ToolGateway{}).
		Owns(&gatewayv1.Gateway{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Owns(agentgatewayParams).
		Watches(
			&agentruntimev1alpha1.ToolServer{},
			handler.EnqueueRequestsFromMapFunc(r.findToolGatewayForToolServer),
		).
		Named(ToolGatewayAgentgatewayControllerName).
		Complete(r)
}

// findToolGatewayForToolServer maps a ToolServer to its associated ToolGateway for reconciliation
func (r *ToolGatewayReconciler) findToolGatewayForToolServer(_ context.Context, obj client.Object) []ctrl.Request {
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
