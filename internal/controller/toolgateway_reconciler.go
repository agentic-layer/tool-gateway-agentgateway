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
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	agentGatewayClassName    = "agentgateway"
	readyConditionType       = "Ready"
	toolGatewayFinalizerName = "runtime.agentic-layer.ai/toolgateway-cleanup"
	// Labels written on cross-namespace AgentgatewayPolicy resources so the
	// finalizer can locate them at deletion time. Keys may contain a `/`
	// prefix, but values must be valid DNS-1123-ish strings (no `/`), so we
	// split the gateway identity into namespace/name labels rather than
	// joining with a slash.
	multiplexPolicyManagedByLabelKey     = "runtime.agentic-layer.ai/managed-by"
	multiplexPolicyManagedByLabelValue   = "tool-gateway-agentgateway-controller"
	multiplexPolicyGatewayNamespaceLabel = "runtime.agentic-layer.ai/toolgateway-namespace"
	multiplexPolicyGatewayNameLabel      = "runtime.agentic-layer.ai/toolgateway-name"
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

// multiplexContribution represents one ToolRoute's participation in a multiplex
// backend: the route identity (drives the multiplex target ID and namespace
// grouping), the resolved upstream coordinates (cluster ToolServer or external
// URL — both are handled identically once resolved), and the route's optional
// ToolFilter that scopes the per-target CEL rule.
//
// The unit of contribution is the ToolRoute, not the ToolServer: two routes
// pointing at the same upstream produce two backend targets and two scoped
// filters, so each route fully owns its own multiplex slice.
type multiplexContribution struct {
	routeKey types.NamespacedName
	host     string
	port     int32
	path     string
	filter   *agentruntimev1alpha1.ToolFilter
}

// targetID returns the multiplex target name for this contribution. agentgateway
// prefixes tool names with this ID at the aggregate endpoints (`/mcp` and
// `/<ns>/mcp`) and exposes it as `mcp.tool.target` for CEL filter scoping.
func (c multiplexContribution) targetID() string {
	return shortID(c.routeKey.Namespace + "/" + c.routeKey.Name)
}

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
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaypolicies,verbs=get;list;watch;create;update;patch;delete
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

	// Handle deletion before the ownership check. Presence of our finalizer is
	// itself proof that we previously claimed this ToolGateway and must clean
	// up — even if the ToolGatewayClass is already gone (a common race during
	// `kubectl delete -f` of a sample bundle, where the class can be reaped
	// before the gateway). Skipping cleanup when ownership can no longer be
	// resolved would leave the finalizer in place forever and block namespace
	// deletion.
	if !toolGateway.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&toolGateway, toolGatewayFinalizerName) {
			if err := r.cleanupCrossNamespaceResources(ctx, &toolGateway); err != nil {
				log.Error(err, "Failed to clean up cross-namespace resources")
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&toolGateway, toolGatewayFinalizerName)
			if err := r.Update(ctx, &toolGateway); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

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

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&toolGateway, toolGatewayFinalizerName) {
		controllerutil.AddFinalizer(&toolGateway, toolGatewayFinalizerName)
		if err := r.Update(ctx, &toolGateway); err != nil {
			return ctrl.Result{}, err
		}
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

	contributions, err := r.getMultiplexContributionsForGateway(ctx, toolGateway)
	if err != nil {
		return fmt.Errorf("failed to get multiplex contributions for gateway: %w", err)
	}

	if len(contributions) == 0 {
		log.Info("No ToolRoutes found for gateway, skipping multiplex routes")
		return nil
	}

	// Group by route namespace: `/<ns>/mcp` aggregates routes defined in <ns>,
	// regardless of where their upstream lives. This makes the namespace-scoped
	// multiplex a function of route placement (the user-facing knob), not of
	// upstream topology.
	byNamespace := map[string][]multiplexContribution{}
	for _, c := range contributions {
		byNamespace[c.routeKey.Namespace] = append(byNamespace[c.routeKey.Namespace], c)
	}
	for ns, nsContribs := range byNamespace {
		name := toolGateway.Name + "-" + ns
		if err := r.ensureMultiplexBackend(ctx, toolGateway, nsContribs, name, ns); err != nil {
			return fmt.Errorf("failed to ensure namespace multiplex backend for %s: %w", ns, err)
		}
		if err := r.ensureMultiplexRoute(ctx, toolGateway, name, ns, fmt.Sprintf("/%s/mcp", ns)); err != nil {
			return fmt.Errorf("failed to ensure namespace multiplex route for %s: %w", ns, err)
		}
		if err := r.ensureMultiplexPolicy(ctx, toolGateway, nsContribs, name, ns); err != nil {
			return fmt.Errorf("failed to ensure namespace multiplex policy for %s: %w", ns, err)
		}
	}

	// Create root-level multiplex backend and route (/mcp)
	if err := r.ensureMultiplexBackend(ctx, toolGateway, contributions, toolGateway.Name, toolGateway.Namespace); err != nil {
		return fmt.Errorf("failed to ensure root multiplex backend: %w", err)
	}

	if err := r.ensureMultiplexRoute(ctx, toolGateway, toolGateway.Name, toolGateway.Namespace, "/mcp"); err != nil {
		return fmt.Errorf("failed to ensure root multiplex route: %w", err)
	}

	if err := r.ensureMultiplexPolicy(ctx, toolGateway, contributions, toolGateway.Name, toolGateway.Namespace); err != nil {
		return fmt.Errorf("failed to ensure root multiplex policy: %w", err)
	}

	return nil
}

// getMultiplexContributionsForGateway returns the contributions exposed to this
// ToolGateway via ToolRoutes. Each ToolRoute contributes exactly one entry —
// no dedup by upstream — so every route's filter is independently honored even
// when two routes point at the same ToolServer or external URL.
//
// Routes with a nil ToolGatewayRef are matched when this gateway is their
// resolved default. Routes whose upstream cannot be resolved (missing
// ToolServer, malformed external URL) are silently omitted; the per-route
// reconciler surfaces those failures on the route's status.
//
// Results are sorted by namespace/name for deterministic backend target order
// and a deterministic AND-joined CEL clause sequence.
func (r *ToolGatewayReconciler) getMultiplexContributionsForGateway(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) ([]multiplexContribution, error) {
	var toolRouteList agentruntimev1alpha1.ToolRouteList
	if err := r.List(ctx, &toolRouteList); err != nil {
		return nil, fmt.Errorf("failed to list ToolRoutes: %w", err)
	}

	sort.Slice(toolRouteList.Items, func(i, j int) bool {
		if toolRouteList.Items[i].Namespace != toolRouteList.Items[j].Namespace {
			return toolRouteList.Items[i].Namespace < toolRouteList.Items[j].Namespace
		}
		return toolRouteList.Items[i].Name < toolRouteList.Items[j].Name
	})

	gwKey := types.NamespacedName{Name: toolGateway.Name, Namespace: toolGateway.Namespace}

	var result []multiplexContribution
	for i := range toolRouteList.Items {
		tr := toolRouteList.Items[i]
		key, err := resolveToolGatewayKey(ctx, r.Client, &tr)
		if err != nil || key != gwKey {
			continue
		}
		host, port, path, err := resolveRouteUpstream(ctx, r.Client, &tr)
		if err != nil {
			continue
		}
		result = append(result, multiplexContribution{
			routeKey: types.NamespacedName{Name: tr.Name, Namespace: tr.Namespace},
			host:     host,
			port:     port,
			path:     path,
			filter:   tr.Spec.ToolFilter,
		})
	}

	return result, nil
}

// ensureMultiplexBackend creates a multiplex backend with one target per
// contribution (i.e. one per ToolRoute). name and namespace specify where the
// AgentgatewayBackend is created. Owner reference is only set when the backend
// is in the same namespace as the ToolGateway, since Kubernetes does not
// support cross-namespace owner references.
func (r *ToolGatewayReconciler) ensureMultiplexBackend(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway, contribs []multiplexContribution, name, namespace string) error {
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

		targets := make([]interface{}, 0, len(contribs))
		for _, c := range contribs {
			targets = append(targets, buildMCPTarget(c.targetID(), c.host, c.port, c.path))
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

// ensureMultiplexPolicy creates or updates the AgentgatewayPolicy attached to
// a multiplex HTTPRoute when at least one contribution carries a ToolFilter,
// and deletes any stale policy otherwise. The CEL rules are scoped per target
// prefix so each route's filter only governs its own tools — tools from other
// servers in the same multiplex are not affected.
//
// routeName/routeNamespace identifies the multiplex HTTPRoute being targeted
// (built by ensureMultiplexRoute with the same name/namespace).
func (r *ToolGatewayReconciler) ensureMultiplexPolicy(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway, contribs []multiplexContribution, routeName, routeNamespace string) error {
	log := logf.FromContext(ctx)

	policyName := routeName + "-toolfilter"
	rules := buildMultiplexAuthorizationRules(multiplexFilterContributions(contribs))

	if len(rules) == 0 {
		existing := newAgentgatewayPolicy(policyName, routeNamespace)
		err := r.Delete(ctx, existing)
		if err == nil {
			log.Info("Deleted stale multiplex tool-filter policy", "name", policyName, "namespace", routeNamespace)
			r.emitNormalEvent(toolGateway, "MultiplexPolicyDeleted", "DeleteMultiplexPolicy",
				"Deleted multiplex AgentgatewayPolicy %s/%s (no filtered routes contribute)", routeNamespace, policyName)
			return nil
		}
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete stale multiplex tool-filter policy: %w", err)
	}

	policy := newAgentgatewayPolicy(policyName, routeNamespace)
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, policy, func() error {
		// Owner reference is only valid within the same namespace; multiplex
		// policies in foreign namespaces are cleaned up via finalizer when the
		// ToolGateway is deleted.
		if routeNamespace == toolGateway.Namespace {
			if e := controllerutil.SetControllerReference(toolGateway, policy, r.Scheme); e != nil {
				return e
			}
		} else {
			// For cross-namespace policies, add labels for garbage collection via finalizer
			if policy.GetLabels() == nil {
				policy.SetLabels(map[string]string{})
			}
			labels := policy.GetLabels()
			labels[multiplexPolicyManagedByLabelKey] = multiplexPolicyManagedByLabelValue
			labels[multiplexPolicyGatewayNamespaceLabel] = toolGateway.Namespace
			labels[multiplexPolicyGatewayNameLabel] = toolGateway.Name
			policy.SetLabels(labels)
		}

		targetRef := map[string]interface{}{
			"group": "gateway.networking.k8s.io",
			"kind":  "HTTPRoute",
			"name":  routeName,
		}
		if e := unstructured.SetNestedSlice(policy.Object, []interface{}{targetRef}, "spec", "targetRefs"); e != nil {
			return e
		}

		matchExprs := make([]interface{}, len(rules))
		for i, s := range rules {
			matchExprs[i] = s
		}
		authorization := map[string]interface{}{
			"action": "Allow",
			"policy": map[string]interface{}{
				"matchExpressions": matchExprs,
			},
		}
		return unstructured.SetNestedMap(policy.Object, authorization, "spec", "backend", "mcp", "authorization")
	})
	if err != nil {
		return fmt.Errorf("failed to ensure multiplex AgentgatewayPolicy: %w", err)
	}

	log.Info("Multiplex AgentgatewayPolicy reconciled", "operation", op, "name", policyName, "namespace", routeNamespace)

	switch op {
	case controllerutil.OperationResultCreated:
		r.emitNormalEvent(toolGateway, "MultiplexPolicyCreated", "CreateMultiplexPolicy",
			"Created multiplex AgentgatewayPolicy %s/%s", routeNamespace, policyName)
	case controllerutil.OperationResultUpdated:
		r.emitNormalEvent(toolGateway, "MultiplexPolicyUpdated", "UpdateMultiplexPolicy",
			"Updated multiplex AgentgatewayPolicy %s/%s", routeNamespace, policyName)
	}
	return nil
}

// multiplexFilterContributions converts multiplex contributions into the
// (target, filter) pairs consumed by buildMultiplexAuthorizationRules. The
// target ID matches the one written into the backend by ensureMultiplexBackend
// (derived from the route's namespace/name), so CEL rules scoped via
// `mcp.tool.target == <id>` line up with the backend slice they govern. Target
// is left empty for single-target multiplexes where scoping is unnecessary.
func multiplexFilterContributions(contribs []multiplexContribution) []MultiplexFilterContribution {
	out := make([]MultiplexFilterContribution, 0, len(contribs))
	scoped := len(contribs) > 1
	for _, c := range contribs {
		var target string
		if scoped {
			target = c.targetID()
		}
		out = append(out, MultiplexFilterContribution{Target: target, Filter: c.filter})
	}
	return out
}

// cleanupCrossNamespaceResources deletes all AgentgatewayPolicy resources in
// foreign namespaces that were created by this ToolGateway (identified by labels).
// This is called when the ToolGateway is being deleted and ensures no orphaned
// policies remain in other namespaces.
func (r *ToolGatewayReconciler) cleanupCrossNamespaceResources(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) error {
	log := logf.FromContext(ctx)

	// List all AgentgatewayPolicy resources with our labels
	var policyList unstructured.UnstructuredList
	policyList.SetAPIVersion("agentgateway.dev/v1alpha1")
	policyList.SetKind("AgentgatewayPolicyList")

	if err := r.List(ctx, &policyList, client.MatchingLabels{
		multiplexPolicyManagedByLabelKey:     multiplexPolicyManagedByLabelValue,
		multiplexPolicyGatewayNamespaceLabel: toolGateway.Namespace,
		multiplexPolicyGatewayNameLabel:      toolGateway.Name,
	}); err != nil {
		return fmt.Errorf("failed to list cross-namespace AgentgatewayPolicies: %w", err)
	}

	for i := range policyList.Items {
		policy := &policyList.Items[i]
		// Only delete policies in other namespaces (same-namespace ones have owner refs)
		if policy.GetNamespace() != toolGateway.Namespace {
			if err := r.Delete(ctx, policy); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete cross-namespace AgentgatewayPolicy %s/%s: %w",
					policy.GetNamespace(), policy.GetName(), err)
			}
			log.Info("Deleted cross-namespace AgentgatewayPolicy", "name", policy.GetName(), "namespace", policy.GetNamespace())
		}
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

// findToolGatewayForToolRoute maps a ToolRoute to its referenced ToolGateway
// for reconciliation. When the route has no explicit ToolGatewayRef, the
// default ToolGateway (if any) is resolved and enqueued; if no default can be
// determined we return nothing, since there's no concrete gateway to wake up.
func (r *ToolGatewayReconciler) findToolGatewayForToolRoute(ctx context.Context, obj client.Object) []ctrl.Request {
	toolRoute, ok := obj.(*agentruntimev1alpha1.ToolRoute)
	if !ok {
		return nil
	}
	key, err := resolveToolGatewayKey(ctx, r.Client, toolRoute)
	if err != nil {
		return nil
	}
	return []ctrl.Request{{NamespacedName: key}}
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
	for i := range toolRouteList.Items {
		tr := toolRouteList.Items[i]
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
		key, err := resolveToolGatewayKey(ctx, r.Client, &tr)
		if err != nil {
			continue
		}
		gateways[key] = true
	}

	var requests []ctrl.Request
	for key := range gateways {
		requests = append(requests, ctrl.Request{NamespacedName: key})
	}
	return requests
}
