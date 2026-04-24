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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	readyConditionType    = "Ready"
)

// Condition reasons for ToolGateway/ToolRoute Ready conditions. Kept deliberately
// fine-grained so `kubectl get` / `describe` / watchers can differentiate which
// phase of reconciliation failed without parsing the message.
const (
	reasonReconciled                   = "Reconciled"
	reasonAgentgatewayParametersFailed = "AgentgatewayParametersReconciliationFailed"
	reasonGatewayFailed                = "GatewayReconciliationFailed"
	reasonMultiplexRoutesFailed        = "MultiplexRoutesReconciliationFailed"
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
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolroutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaybackends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewayparameters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

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

	// Check if this controller should process this ToolGateway. When ownership
	// cannot be determined (API error), return the error so the reconcile is
	// requeued and the object isn't silently dropped.
	owned, err := isToolGatewayOwnedByController(ctx, r.Client, &toolGateway)
	if err != nil {
		log.Error(err, "Failed to determine controller ownership")
		return ctrl.Result{}, err
	}
	if !owned {
		log.Info("Controller is not responsible for this ToolGateway, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Run core reconciliation and always mirror the outcome to the CR status.
	reason, reconcileErr := r.reconcileToolGateway(ctx, &toolGateway)
	if reconcileErr != nil {
		log.Error(reconcileErr, "Reconciliation failed", "reason", reason)
		if statusErr := r.updateStatusNotReady(ctx, &toolGateway, reason, reconcileErr.Error()); statusErr != nil {
			log.Error(statusErr, "Failed to update ToolGateway status to NotReady")
		}
		return ctrl.Result{}, reconcileErr
	}

	if err := r.updateStatusReady(ctx, &toolGateway); err != nil {
		log.Error(err, "Failed to update ToolGateway status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileToolGateway performs the core reconciliation work. Returns a reason
// string classifying which phase failed (used to set a descriptive Ready=False
// reason) and the underlying error. Reason is empty on success.
func (r *ToolGatewayReconciler) reconcileToolGateway(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) (string, error) {
	if err := r.ensureAgentgatewayParameters(ctx, toolGateway); err != nil {
		return reasonAgentgatewayParametersFailed, err
	}

	if err := r.ensureGateway(ctx, toolGateway); err != nil {
		return reasonGatewayFailed, err
	}

	if err := r.ensureMultiplexRoutes(ctx, toolGateway); err != nil {
		return reasonMultiplexRoutesFailed, err
	}

	return "", nil
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

	// Only emit events on actual resource changes, not on every reconcile.
	// This keeps the event stream bounded in steady state.
	switch op {
	case controllerutil.OperationResultCreated:
		r.emitNormalEvent(toolGateway, "GatewayCreated", "CreateGateway",
			"Created Gateway %s", gateway.Name)
	case controllerutil.OperationResultUpdated:
		r.emitNormalEvent(toolGateway, "GatewayUpdated", "UpdateGateway",
			"Updated Gateway %s", gateway.Name)
	}

	return nil
}

// updateStatusReady marks the ToolGateway Ready=True and records the URL.
// Uses Patch with MergeFrom so concurrent updates by other controllers do not
// conflict (resourceVersion is not included in the patch body).
func (r *ToolGatewayReconciler) updateStatusReady(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) error {
	patch := client.MergeFrom(toolGateway.DeepCopy())

	toolGateway.Status.Url = fmt.Sprintf("http://%s.%s.svc.cluster.local", toolGateway.Name, toolGateway.Namespace)
	apimeta.SetStatusCondition(&toolGateway.Status.Conditions, metav1.Condition{
		Type:               readyConditionType,
		Status:             metav1.ConditionTrue,
		Reason:             reasonReconciled,
		Message:            "Gateway and its backing resources are configured",
		ObservedGeneration: toolGateway.Generation,
	})

	if err := r.Status().Patch(ctx, toolGateway, patch); err != nil {
		return fmt.Errorf("failed to patch ToolGateway status: %w", err)
	}
	return nil
}

// updateStatusNotReady marks the ToolGateway Ready=False with a phase-specific
// reason and the concrete error message. The URL is intentionally left alone
// (possibly stale) so downstream consumers can still see the last-known URL
// while the operator recovers.
func (r *ToolGatewayReconciler) updateStatusNotReady(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway, reason, message string) error {
	patch := client.MergeFrom(toolGateway.DeepCopy())

	apimeta.SetStatusCondition(&toolGateway.Status.Conditions, metav1.Condition{
		Type:               readyConditionType,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: toolGateway.Generation,
	})

	if err := r.Status().Patch(ctx, toolGateway, patch); err != nil {
		return fmt.Errorf("failed to patch ToolGateway status: %w", err)
	}
	return nil
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
		r.emitNormalEvent(toolGateway, "AgentgatewayParametersCreated", "CreateAgentgatewayParameters",
			"Created AgentgatewayParameters %s", params.GetName())
	case controllerutil.OperationResultUpdated:
		r.emitNormalEvent(toolGateway, "AgentgatewayParametersUpdated", "UpdateAgentgatewayParameters",
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

// getToolServersForGateway retrieves all ToolServers exposed to this ToolGateway via ToolRoutes.
// Only ToolRoutes with an upstream.toolServerRef contribute; external upstreams are skipped.
// Duplicate ToolServers (referenced by multiple routes) are de-duplicated.
func (r *ToolGatewayReconciler) getToolServersForGateway(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) ([]agentruntimev1alpha1.ToolServer, error) {
	var toolRouteList agentruntimev1alpha1.ToolRouteList
	if err := r.List(ctx, &toolRouteList); err != nil {
		return nil, fmt.Errorf("failed to list ToolRoutes: %w", err)
	}

	var result []agentruntimev1alpha1.ToolServer
	seen := map[string]bool{}
	for _, tr := range toolRouteList.Items {
		gwNs := tr.Spec.ToolGatewayRef.Namespace
		if gwNs == "" {
			gwNs = tr.Namespace
		}
		if tr.Spec.ToolGatewayRef.Name != toolGateway.Name || gwNs != toolGateway.Namespace {
			continue
		}
		if tr.Spec.Upstream.ToolServerRef == nil {
			continue
		}
		tsNs := tr.Spec.Upstream.ToolServerRef.Namespace
		if tsNs == "" {
			tsNs = tr.Namespace
		}
		key := tsNs + "/" + tr.Spec.Upstream.ToolServerRef.Name
		if seen[key] {
			continue
		}
		seen[key] = true

		var ts agentruntimev1alpha1.ToolServer
		if err := r.Get(ctx, types.NamespacedName{Name: tr.Spec.Upstream.ToolServerRef.Name, Namespace: tsNs}, &ts); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("failed to get ToolServer %s/%s: %w", tsNs, tr.Spec.Upstream.ToolServerRef.Name, err)
		}
		result = append(result, ts)
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
				shortID(fmt.Sprintf("%s/%s", ts.Namespace, ts.Name)),
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
		r.emitNormalEvent(toolGateway, "MultiplexBackendCreated", "CreateMultiplexBackend",
			"Created multiplex backend %s/%s", namespace, backend.GetName())
	case controllerutil.OperationResultUpdated:
		r.emitNormalEvent(toolGateway, "MultiplexBackendUpdated", "UpdateMultiplexBackend",
			"Updated multiplex backend %s/%s", namespace, backend.GetName())
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
		r.emitNormalEvent(toolGateway, "MultiplexRouteCreated", "CreateMultiplexRoute",
			"Created multiplex route %s/%s", namespace, route.Name)
	case controllerutil.OperationResultUpdated:
		r.emitNormalEvent(toolGateway, "MultiplexRouteUpdated", "UpdateMultiplexRoute",
			"Updated multiplex route %s/%s", namespace, route.Name)
	}

	return nil
}

// emitNormalEvent records a Normal Kubernetes event. Guarded by nil-check so
// reconcilers remain usable in tests that don't wire a Recorder.
// Warning events are intentionally NOT emitted on reconciliation errors to
// avoid spamming on persistent failures; the Ready=False condition with a
// phase-specific reason is the source of truth for user-visible error state.
func (r *ToolGatewayReconciler) emitNormalEvent(regarding runtime.Object, reason, action, note string, args ...interface{}) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(regarding, nil, corev1.EventTypeNormal, reason, action, note, args...)
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
			&agentruntimev1alpha1.ToolRoute{},
			handler.EnqueueRequestsFromMapFunc(r.findToolGatewayForToolRoute),
		).
		Watches(
			&agentruntimev1alpha1.ToolServer{},
			handler.EnqueueRequestsFromMapFunc(r.findToolGatewaysForToolServer),
		).
		Named(ToolGatewayAgentgatewayControllerName).
		Complete(r)
}

// findToolGatewayForToolRoute maps a ToolRoute to its referenced ToolGateway for reconciliation.
func (r *ToolGatewayReconciler) findToolGatewayForToolRoute(_ context.Context, obj client.Object) []ctrl.Request {
	toolRoute, ok := obj.(*agentruntimev1alpha1.ToolRoute)
	if !ok {
		return nil
	}
	ns := toolRoute.Spec.ToolGatewayRef.Namespace
	if ns == "" {
		ns = toolRoute.Namespace
	}
	return []ctrl.Request{
		{
			NamespacedName: client.ObjectKey{
				Name:      toolRoute.Spec.ToolGatewayRef.Name,
				Namespace: ns,
			},
		},
	}
}

// findToolGatewaysForToolServer maps a ToolServer to all ToolGateways that reference it via ToolRoutes.
// This ensures multiplex backends/routes are updated when a ToolServer's port/path changes.
func (r *ToolGatewayReconciler) findToolGatewaysForToolServer(ctx context.Context, obj client.Object) []ctrl.Request {
	toolServer, ok := obj.(*agentruntimev1alpha1.ToolServer)
	if !ok {
		return nil
	}

	// List all ToolRoutes that reference this ToolServer
	var toolRouteList agentruntimev1alpha1.ToolRouteList
	if err := r.List(ctx, &toolRouteList); err != nil {
		return nil
	}

	// Find unique ToolGateways referenced by those ToolRoutes
	gateways := map[client.ObjectKey]bool{}
	for _, tr := range toolRouteList.Items {
		if tr.Spec.Upstream.ToolServerRef == nil {
			continue
		}
		tsNs := tr.Spec.Upstream.ToolServerRef.Namespace
		if tsNs == "" {
			tsNs = tr.Namespace
		}
		if tr.Spec.Upstream.ToolServerRef.Name != toolServer.Name || tsNs != toolServer.Namespace {
			continue
		}
		gwNs := tr.Spec.ToolGatewayRef.Namespace
		if gwNs == "" {
			gwNs = tr.Namespace
		}
		gateways[client.ObjectKey{
			Name:      tr.Spec.ToolGatewayRef.Name,
			Namespace: gwNs,
		}] = true
	}

	var requests []ctrl.Request
	for key := range gateways {
		requests = append(requests, ctrl.Request{NamespacedName: key})
	}
	return requests
}
