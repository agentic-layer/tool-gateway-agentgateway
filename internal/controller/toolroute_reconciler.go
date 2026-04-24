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
	"net/url"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

const ToolRouteAgentgatewayControllerName = "runtime.agentic-layer.ai/toolroute-agentgateway-controller"

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
// AgentgatewayPolicy for a ToolRoute in sync with the spec.
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

	gatewayNs := route.Spec.ToolGatewayRef.Namespace
	if gatewayNs == "" {
		gatewayNs = route.Namespace
	}

	var toolGateway agentruntimev1alpha1.ToolGateway
	if err := r.Get(ctx, types.NamespacedName{Name: route.Spec.ToolGatewayRef.Name, Namespace: gatewayNs}, &toolGateway); err != nil {
		if apierrors.IsNotFound(err) {
			r.Recorder.Eventf(&route, nil, "Warning", "GatewayNotFound", "GatewayNotFound",
				"ToolGateway %s/%s not found", gatewayNs, route.Spec.ToolGatewayRef.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !r.shouldProcess(ctx, &toolGateway) {
		log.Info("ToolRoute targets a gateway not owned by this controller, skipping",
			"toolGateway", toolGateway.Name, "class", toolGateway.Spec.ToolGatewayClassName)
		return ctrl.Result{}, nil
	}

	host, port, path, err := r.resolveUpstream(ctx, &route)
	if err != nil {
		r.Recorder.Eventf(&route, nil, "Warning", "UpstreamUnresolved", "UpstreamUnresolved", "%s", err.Error())
		return ctrl.Result{}, err
	}

	if err := r.ensureBackend(ctx, &route, &toolGateway, host, port, path); err != nil {
		return ctrl.Result{}, err
	}

	routePath := fmt.Sprintf("/%s/%s/mcp", route.Namespace, route.Name)
	if err := r.ensureHTTPRoute(ctx, &route, &toolGateway, routePath); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensurePolicy(ctx, &route, &toolGateway); err != nil {
		return ctrl.Result{}, err
	}

	return r.updateStatus(ctx, &route, &toolGateway, routePath)
}

// shouldProcess returns true when the referenced ToolGateway is owned by this controller.
// Matches ToolGatewayReconciler.shouldProcessToolGateway: explicit className must match an owned class,
// otherwise defaults to checking for an owned default class.
func (r *ToolRouteReconciler) shouldProcess(ctx context.Context, tg *agentruntimev1alpha1.ToolGateway) bool {
	log := logf.FromContext(ctx)

	var classList agentruntimev1alpha1.ToolGatewayClassList
	if err := r.List(ctx, &classList); err != nil {
		log.Error(err, "Failed to list ToolGatewayClasses")
		return false
	}

	var owned []agentruntimev1alpha1.ToolGatewayClass
	for _, tgc := range classList.Items {
		if tgc.Spec.Controller == ToolGatewayAgentgatewayControllerName {
			owned = append(owned, tgc)
		}
	}

	className := tg.Spec.ToolGatewayClassName
	if className != "" {
		for _, c := range owned {
			if c.Name == className {
				return true
			}
		}
		return false
	}

	// Unclassed ToolGateway: claim iff a default class is owned by this controller.
	for _, c := range owned {
		if c.Annotations["toolgatewayclass.kubernetes.io/is-default-class"] == "true" {
			return true
		}
	}
	return false
}

// resolveUpstream returns the MCP target host, port, and path derived from the route's upstream spec.
func (r *ToolRouteReconciler) resolveUpstream(ctx context.Context, route *agentruntimev1alpha1.ToolRoute) (host string, port int32, path string, err error) {
	switch {
	case route.Spec.Upstream.ToolServerRef != nil:
		ref := route.Spec.Upstream.ToolServerRef
		ns := ref.Namespace
		if ns == "" {
			ns = route.Namespace
		}
		var ts agentruntimev1alpha1.ToolServer
		if e := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ns}, &ts); e != nil {
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
	backend := newAgentgatewayBackend(tg.Name+"-"+route.Name, route.Namespace)
	_, err := controllerutil.CreateOrPatch(ctx, r.Client, backend, func() error {
		if e := controllerutil.SetControllerReference(route, backend, r.Scheme); e != nil {
			return e
		}
		target := buildMCPTarget("mcp-target", host, port, path)
		return setMCPTargets(backend, []interface{}{target})
	})
	if err != nil {
		return fmt.Errorf("failed to ensure AgentgatewayBackend: %w", err)
	}
	return nil
}

func (r *ToolRouteReconciler) ensureHTTPRoute(ctx context.Context, route *agentruntimev1alpha1.ToolRoute, tg *agentruntimev1alpha1.ToolGateway, routePath string) error {
	hr := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tg.Name + "-" + route.Name,
			Namespace: route.Namespace,
		},
	}
	_, err := controllerutil.CreateOrPatch(ctx, r.Client, hr, func() error {
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
	return nil
}

// ensurePolicy creates/updates the AgentgatewayPolicy carrying mcpAuthorization CEL rules
// translated from route.spec.toolFilter. When toolFilter is nil (or empty), any previously
// created policy is deleted so tools pass through unfiltered.
func (r *ToolRouteReconciler) ensurePolicy(ctx context.Context, route *agentruntimev1alpha1.ToolRoute, tg *agentruntimev1alpha1.ToolGateway) error {
	policyName := tg.Name + "-" + route.Name + "-toolfilter"

	rules := buildMcpAuthorizationRules(route.Spec.ToolFilter)

	if len(rules) == 0 {
		existing := newAgentgatewayPolicy(policyName, route.Namespace)
		if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete stale tool-filter policy: %w", err)
		}
		return nil
	}

	policy := newAgentgatewayPolicy(policyName, route.Namespace)
	_, err := controllerutil.CreateOrPatch(ctx, r.Client, policy, func() error {
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
	return nil
}

func (r *ToolRouteReconciler) updateStatus(ctx context.Context, route *agentruntimev1alpha1.ToolRoute, tg *agentruntimev1alpha1.ToolGateway, routePath string) (ctrl.Result, error) {
	patch := client.MergeFrom(route.DeepCopy())
	gwURL := fmt.Sprintf("http://%s.%s.svc.cluster.local%s", tg.Name, tg.Namespace, routePath)
	route.Status.Url = gwURL
	if err := r.Status().Patch(ctx, route, patch); err != nil {
		return ctrl.Result{}, err
	}
	r.Recorder.Eventf(route, nil, "Normal", "Ready", "Ready", "ToolRoute ready at %s", gwURL)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ToolRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentruntimev1alpha1.ToolRoute{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Named(ToolRouteAgentgatewayControllerName).
		Complete(r)
}
