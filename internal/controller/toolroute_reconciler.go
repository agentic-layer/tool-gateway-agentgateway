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
	"errors"
	"fmt"
	"net/url"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

const ToolRouteAgentgatewayControllerName = "runtime.agentic-layer.ai/toolroute-agentgateway-controller"

// Condition reasons scoped to ToolRoute. Kept separate from ToolGateway
// reasons so each CR's kubectl output and watchers can filter cleanly.
const (
	reasonToolRouteGatewayNotFound    = "GatewayNotFound"
	reasonToolRouteUpstreamUnresolved = "UpstreamUnresolved"
	reasonToolRouteBackendFailed      = "BackendReconciliationFailed"
	reasonToolRouteHTTPRouteFailed    = "HTTPRouteReconciliationFailed"
	reasonToolRoutePolicyFailed       = "PolicyReconciliationFailed"
)

// ToolRouteReconciler reconciles a ToolRoute object.
type ToolRouteReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder kevents.EventRecorder
}

// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolroutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolroutes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolgatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=toolservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaybackends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaypolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile keeps the AgentgatewayBackend, HTTPRoute, and (optionally)
// AgentgatewayPolicy for a ToolRoute in sync with the spec, and mirrors the
// reconciliation outcome to the ToolRoute status (Ready condition + URL).
func (r *ToolRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var route agentruntimev1alpha1.ToolRoute
	if err := r.Get(ctx, req.NamespacedName, &route); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	log.Info("Reconciling ToolRoute", "name", route.Name, "namespace", route.Namespace)

	gatewayKey, err := resolveToolGatewayKey(ctx, r.Client, &route)
	if err != nil {
		// No default ToolGateway exists (yet). The ToolGateway watch will
		// retrigger reconciliation when one is created in the default
		// namespace; for any other lookup error, surface it.
		if errors.Is(err, errNoDefaultToolGateway) {
			if statusErr := r.updateStatusNotReady(ctx, &route, reasonToolRouteGatewayNotFound, err.Error()); statusErr != nil {
				log.Error(statusErr, "Failed to update ToolRoute status to NotReady")
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	var toolGateway agentruntimev1alpha1.ToolGateway
	err = r.Get(ctx, gatewayKey, &toolGateway)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if apierrors.IsNotFound(err) {
		// The explicitly referenced ToolGateway does not exist yet. Surface
		// this clearly on the status so users know why the route isn't Ready.
		// A watch on ToolGateway (see SetupWithManager) will retrigger
		// reconciliation once it is created; no requeue/event spam.
		msg := fmt.Sprintf("ToolGateway %s/%s not found", gatewayKey.Namespace, gatewayKey.Name)
		if statusErr := r.updateStatusNotReady(ctx, &route, reasonToolRouteGatewayNotFound, msg); statusErr != nil {
			log.Error(statusErr, "Failed to update ToolRoute status to NotReady")
		}
		return ctrl.Result{}, nil
	}

	owned, err := isToolGatewayOwnedByController(ctx, r.Client, &toolGateway)
	if err != nil {
		log.Error(err, "Failed to determine controller ownership")
		return ctrl.Result{}, err
	}
	if !owned {
		log.Info("ToolRoute targets a gateway not owned by this controller, skipping",
			"toolGateway", toolGateway.Name, "class", toolGateway.Spec.ToolGatewayClassName)
		// Don't touch status: another controller may be reconciling this
		// ToolRoute and we must not fight over its conditions.
		return ctrl.Result{}, nil
	}

	// Run core reconciliation and always mirror the outcome to the CR status.
	routePath, reason, reconcileErr := r.reconcileToolRoute(ctx, &route, &toolGateway)
	if reconcileErr != nil {
		log.Error(reconcileErr, "Reconciliation failed", "reason", reason)
		if statusErr := r.updateStatusNotReady(ctx, &route, reason, reconcileErr.Error()); statusErr != nil {
			log.Error(statusErr, "Failed to update ToolRoute status to NotReady")
		}
		return ctrl.Result{}, reconcileErr
	}

	if err := r.updateStatusReady(ctx, &route, &toolGateway, routePath); err != nil {
		log.Error(err, "Failed to update ToolRoute status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileToolRoute performs the core reconciliation work. Returns the
// assigned route path, a reason classifying which phase failed, and the
// underlying error. Reason is empty on success.
func (r *ToolRouteReconciler) reconcileToolRoute(ctx context.Context, route *agentruntimev1alpha1.ToolRoute, toolGateway *agentruntimev1alpha1.ToolGateway) (routePath string, reason string, err error) {
	host, port, path, err := resolveRouteUpstream(ctx, r.Client, route)
	if err != nil {
		return "", reasonToolRouteUpstreamUnresolved, err
	}

	if err := r.ensureBackend(ctx, route, toolGateway, host, port, path); err != nil {
		return "", reasonToolRouteBackendFailed, err
	}

	routePath = fmt.Sprintf("/%s/%s/mcp", route.Namespace, route.Name)
	if err := r.ensureHTTPRoute(ctx, route, toolGateway, routePath); err != nil {
		return "", reasonToolRouteHTTPRouteFailed, err
	}

	if err := r.ensurePolicy(ctx, route, toolGateway); err != nil {
		return "", reasonToolRoutePolicyFailed, err
	}

	return routePath, "", nil
}

// resolveRouteUpstream returns the MCP target host, port, and path derived from
// the route's upstream spec.
func resolveRouteUpstream(ctx context.Context, c client.Client, route *agentruntimev1alpha1.ToolRoute) (host string, port int32, path string, err error) {
	switch {
	case route.Spec.Upstream.ToolServerRef != nil:
		ref := route.Spec.Upstream.ToolServerRef
		ns := ref.Namespace
		if ns == "" {
			ns = route.Namespace
		}
		var ts agentruntimev1alpha1.ToolServer
		if e := c.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ns}, &ts); e != nil {
			return "", 0, "", fmt.Errorf("failed to resolve ToolServer %s/%s: %w", ns, ref.Name, e)
		}
		p := ts.Spec.Path
		if p == "" {
			p = "/mcp"
		}
		return toolServerHost(ts.Name, ts.Namespace), ts.Spec.Port, p, nil

	case route.Spec.Upstream.External != nil:
		u, e := url.Parse(route.Spec.Upstream.External.Url)
		if e != nil {
			return "", 0, "", fmt.Errorf("invalid external.url: %w", e)
		}
		// Validate scheme
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", 0, "", fmt.Errorf("invalid external.url scheme %q: must be http or https", u.Scheme)
		}
		// Validate hostname
		h := u.Hostname()
		if h == "" {
			return "", 0, "", fmt.Errorf("invalid external.url: hostname is empty")
		}
		// Parse and validate port
		portStr := u.Port()
		var p int32
		if portStr == "" {
			if u.Scheme == "https" {
				p = 443
			} else {
				p = 80
			}
		} else {
			n, convErr := strconv.Atoi(portStr)
			if convErr != nil {
				return "", 0, "", fmt.Errorf("invalid external.url port: %w", convErr)
			}
			if n < 1 || n > 65535 {
				return "", 0, "", fmt.Errorf("invalid external.url port %d: must be in range 1-65535", n)
			}
			p = int32(n)
		}
		rp := u.Path
		if rp == "" {
			rp = "/"
		}
		return h, p, rp, nil

	default:
		return "", 0, "", fmt.Errorf("upstream has neither toolServerRef nor external set")
	}
}

func (r *ToolRouteReconciler) ensureBackend(ctx context.Context, route *agentruntimev1alpha1.ToolRoute, tg *agentruntimev1alpha1.ToolGateway, host string, port int32, path string) error {
	log := logf.FromContext(ctx)

	backend := newAgentgatewayBackend(tg.Name+"-"+route.Name, route.Namespace)
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, backend, func() error {
		if e := controllerutil.SetControllerReference(route, backend, r.Scheme); e != nil {
			return e
		}
		target := buildMCPTarget("mcp-target", host, port, path)
		return setMCPTargets(backend, []interface{}{target})
	})
	if err != nil {
		return fmt.Errorf("failed to ensure AgentgatewayBackend: %w", err)
	}

	log.Info("AgentgatewayBackend reconciled", "operation", op, "name", backend.GetName())

	switch op {
	case controllerutil.OperationResultCreated:
		r.emitNormalEvent(route, "BackendCreated", "CreateBackend",
			"Created AgentgatewayBackend %s", backend.GetName())
	case controllerutil.OperationResultUpdated:
		r.emitNormalEvent(route, "BackendUpdated", "UpdateBackend",
			"Updated AgentgatewayBackend %s", backend.GetName())
	}
	return nil
}

func (r *ToolRouteReconciler) ensureHTTPRoute(ctx context.Context, route *agentruntimev1alpha1.ToolRoute, tg *agentruntimev1alpha1.ToolGateway, routePath string) error {
	log := logf.FromContext(ctx)

	hr := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tg.Name + "-" + route.Name,
			Namespace: route.Namespace,
		},
	}
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, hr, func() error {
		if e := controllerutil.SetControllerReference(route, hr, r.Scheme); e != nil {
			return e
		}
		hr.Spec = buildHTTPRouteSpec(
			tg.Name, tg.Namespace,
			tg.Name+"-"+route.Name, route.Namespace,
			routePath,
		)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to ensure HTTPRoute: %w", err)
	}

	log.Info("HTTPRoute reconciled", "operation", op, "name", hr.Name)

	switch op {
	case controllerutil.OperationResultCreated:
		r.emitNormalEvent(route, "HTTPRouteCreated", "CreateHTTPRoute",
			"Created HTTPRoute %s", hr.Name)
	case controllerutil.OperationResultUpdated:
		r.emitNormalEvent(route, "HTTPRouteUpdated", "UpdateHTTPRoute",
			"Updated HTTPRoute %s", hr.Name)
	}
	return nil
}

// ensurePolicy creates/updates the AgentgatewayPolicy carrying mcpAuthorization CEL rules
// translated from route.spec.toolFilter. When toolFilter is nil (or empty), any previously
// created policy is deleted so tools pass through unfiltered.
func (r *ToolRouteReconciler) ensurePolicy(ctx context.Context, route *agentruntimev1alpha1.ToolRoute, tg *agentruntimev1alpha1.ToolGateway) error {
	log := logf.FromContext(ctx)

	policyName := tg.Name + "-" + route.Name + "-toolfilter"

	rules := buildMcpAuthorizationRules(route.Spec.ToolFilter)

	if len(rules) == 0 {
		existing := newAgentgatewayPolicy(policyName, route.Namespace)
		err := r.Delete(ctx, existing)
		if err == nil {
			log.Info("Deleted stale tool-filter policy", "name", policyName)
			r.emitNormalEvent(route, "PolicyDeleted", "DeletePolicy",
				"Deleted AgentgatewayPolicy %s (tool filter cleared)", policyName)
			return nil
		}
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete stale tool-filter policy: %w", err)
	}

	policy := newAgentgatewayPolicy(policyName, route.Namespace)
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, policy, func() error {
		if e := controllerutil.SetControllerReference(route, policy, r.Scheme); e != nil {
			return e
		}

		targetRef := map[string]interface{}{
			"group": "gateway.networking.k8s.io",
			"kind":  "HTTPRoute",
			"name":  tg.Name + "-" + route.Name,
		}
		if e := unstructured.SetNestedSlice(policy.Object, []interface{}{targetRef}, "spec", "targetRefs"); e != nil {
			return e
		}

		// AgentgatewayPolicy schema:
		//   spec.backend.mcp.authorization.action: Allow | Deny (default Allow)
		//   spec.backend.mcp.authorization.policy.matchExpressions: [CEL strings]
		// Items matching ANY expression are allowed (under action=Allow). We emit a single
		// compound expression so our glob-to-CEL translator keeps full control of semantics.
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
		if e := unstructured.SetNestedMap(policy.Object, authorization, "spec", "backend", "mcp", "authorization"); e != nil {
			return e
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to ensure AgentgatewayPolicy: %w", err)
	}

	log.Info("AgentgatewayPolicy reconciled", "operation", op, "name", policyName)

	switch op {
	case controllerutil.OperationResultCreated:
		r.emitNormalEvent(route, "PolicyCreated", "CreatePolicy",
			"Created AgentgatewayPolicy %s", policyName)
	case controllerutil.OperationResultUpdated:
		r.emitNormalEvent(route, "PolicyUpdated", "UpdatePolicy",
			"Updated AgentgatewayPolicy %s", policyName)
	}
	return nil
}

// updateStatusReady marks the ToolRoute Ready=True and records its public URL.
// Emits a Normal event only when Ready transitions True (not on every reconcile)
// so a stable route doesn't spam events every loop.
func (r *ToolRouteReconciler) updateStatusReady(ctx context.Context, route *agentruntimev1alpha1.ToolRoute, tg *agentruntimev1alpha1.ToolGateway, routePath string) error {
	wasReady := apimeta.IsStatusConditionTrue(route.Status.Conditions, readyConditionType)

	patch := client.MergeFrom(route.DeepCopy())
	gwURL := fmt.Sprintf("http://%s.%s.svc.cluster.local%s", tg.Name, tg.Namespace, routePath)
	route.Status.Url = gwURL
	apimeta.SetStatusCondition(&route.Status.Conditions, metav1.Condition{
		Type:               readyConditionType,
		Status:             metav1.ConditionTrue,
		Reason:             reasonReconciled,
		Message:            fmt.Sprintf("ToolRoute is ready at %s", gwURL),
		ObservedGeneration: route.Generation,
	})

	if err := r.Status().Patch(ctx, route, patch); err != nil {
		return fmt.Errorf("failed to patch ToolRoute status: %w", err)
	}

	if !wasReady {
		r.emitNormalEvent(route, "Ready", "Reconciled", "ToolRoute ready at %s", gwURL)
	}
	return nil
}

// updateStatusNotReady marks the ToolRoute Ready=False with a phase-specific
// reason. Uses Patch with MergeFrom so stale object versions don't conflict.
func (r *ToolRouteReconciler) updateStatusNotReady(ctx context.Context, route *agentruntimev1alpha1.ToolRoute, reason, message string) error {
	patch := client.MergeFrom(route.DeepCopy())

	apimeta.SetStatusCondition(&route.Status.Conditions, metav1.Condition{
		Type:               readyConditionType,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: route.Generation,
	})

	if err := r.Status().Patch(ctx, route, patch); err != nil {
		return fmt.Errorf("failed to patch ToolRoute status: %w", err)
	}
	return nil
}

// emitNormalEvent records a Normal Kubernetes event. Nil-Recorder-safe.
// Warning events are intentionally NOT emitted on reconciliation errors to
// avoid spamming on persistent failures; the Ready=False condition with a
// phase-specific reason is the user-facing source of truth for error state.
func (r *ToolRouteReconciler) emitNormalEvent(regarding runtime.Object, reason, action, note string, args ...interface{}) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(regarding, nil, corev1.EventTypeNormal, reason, action, note, args...)
}

// SetupWithManager sets up the controller with the Manager. Watching
// ToolGateway ensures a ToolRoute that was blocked on a missing gateway is
// re-reconciled once that gateway appears, without requiring periodic requeue.
// Watching ToolServer ensures ToolRoutes are reconciled when their referenced
// ToolServer's port/path changes, keeping AgentgatewayBackends up to date.
func (r *ToolRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentruntimev1alpha1.ToolRoute{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Watches(
			&agentruntimev1alpha1.ToolGateway{},
			handler.EnqueueRequestsFromMapFunc(r.findToolRoutesForToolGateway),
		).
		Watches(
			&agentruntimev1alpha1.ToolServer{},
			handler.EnqueueRequestsFromMapFunc(r.findToolRoutesForToolServer),
		).
		Named(ToolRouteAgentgatewayControllerName).
		Complete(r)
}

// findToolRoutesForToolGateway enqueues every ToolRoute that targets the
// changed ToolGateway. Used so routes waiting on a not-yet-created gateway get
// reconciled as soon as it appears. Routes with no ToolGatewayRef are
// enqueued only when the changed gateway lives in defaultToolGatewayNamespace,
// since that is the only namespace consulted for the default lookup.
func (r *ToolRouteReconciler) findToolRoutesForToolGateway(ctx context.Context, obj client.Object) []ctrl.Request {
	tg, ok := obj.(*agentruntimev1alpha1.ToolGateway)
	if !ok {
		return nil
	}

	var routes agentruntimev1alpha1.ToolRouteList
	if err := r.List(ctx, &routes); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list ToolRoutes for ToolGateway watch")
		return nil
	}

	var requests []ctrl.Request
	for _, route := range routes.Items {
		if route.Spec.ToolGatewayRef == nil {
			if tg.Namespace == defaultToolGatewayNamespace {
				requests = append(requests, ctrl.Request{
					NamespacedName: types.NamespacedName{Name: route.Name, Namespace: route.Namespace},
				})
			}
			continue
		}
		gwNs := route.Spec.ToolGatewayRef.Namespace
		if gwNs == "" {
			gwNs = route.Namespace
		}
		if route.Spec.ToolGatewayRef.Name == tg.Name && gwNs == tg.Namespace {
			requests = append(requests, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: route.Name, Namespace: route.Namespace},
			})
		}
	}
	return requests
}

// findToolRoutesForToolServer enqueues every ToolRoute that references the
// changed ToolServer. Ensures ToolRoute backends are updated when a ToolServer's
// port or path changes.
func (r *ToolRouteReconciler) findToolRoutesForToolServer(ctx context.Context, obj client.Object) []ctrl.Request {
	ts, ok := obj.(*agentruntimev1alpha1.ToolServer)
	if !ok {
		return nil
	}

	var routes agentruntimev1alpha1.ToolRouteList
	if err := r.List(ctx, &routes); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list ToolRoutes for ToolServer watch")
		return nil
	}

	var requests []ctrl.Request
	for _, route := range routes.Items {
		if route.Spec.Upstream.ToolServerRef == nil {
			continue
		}
		tsNs := route.Spec.Upstream.ToolServerRef.Namespace
		if tsNs == "" {
			tsNs = route.Namespace
		}
		if route.Spec.Upstream.ToolServerRef.Name == ts.Name && tsNs == ts.Namespace {
			requests = append(requests, ctrl.Request{
				NamespacedName: types.NamespacedName{Name: route.Name, Namespace: route.Namespace},
			})
		}
	}
	return requests
}
