# Tool Filtering via ToolRoute — `tool-gateway-agentgateway` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Adapt `tool-gateway-agentgateway` to the new `ToolRoute`-centric model: introduce a `ToolRouteReconciler` that creates Gateway API resources per `ToolRoute`, translate `toolFilter` into agentgateway's native filter primitive, and delete the old `ToolServerReconciler` that keyed off `ToolServer.status.ToolGatewayRef`.

**Architecture:** Each `ToolRoute` becomes one `AgentgatewayBackend` (pointing at either a cluster ToolServer Service or an external URL), one `HTTPRoute` attached to the route's `ToolGateway`, and — when `toolFilter` is set — one `AgentgatewayPolicy` with `mcpAuthorization` CEL rules targeting that `HTTPRoute`. Agentgateway has no native allow/deny field on the backend; tool filtering is expressed as CEL (regex/prefix supported), so glob patterns in the CRD are translated to CEL at reconcile time. The reconciler resolves upstream, applies class-based ownership filtering (same pattern as `ToolGatewayReconciler.shouldProcessToolGateway`), builds the filter policy, and populates `ToolRoute.status.url` with the reachable gateway path. Reconciliation of gateway-wide concerns (Gateway, AgentgatewayParameters, guardrails) stays with `ToolGatewayReconciler` and is not touched by this plan.

**Tech Stack:** Go 1.26, controller-runtime, Gateway API, agentgateway CRDs (via `unstructured.Unstructured` — no typed client), envtest, Ginkgo/Gomega. Repo: ``.

**Related spec:** `docs/superpowers/specs/2026-04-22-tool-filtering-toolroute-design.md`

**Dependency:** The companion plan for `agent-runtime-operator` (`docs/superpowers/plans/2026-04-22-tool-filtering-toolroute-agent-runtime-operator.md`) must have landed at least on a branch available to this repo — this plan consumes the new `ToolRoute` CRD type and the stripped `ToolServer` type. During development we use a `replace` directive in `go.mod`; the release cuts assume both repos publish compatible versions in sync.

---

## File Structure

**Created:**
- `internal/controller/toolroute_reconciler.go` — main reconciler (reconcile + class filter + upstream resolve + ensure Backend/HTTPRoute/Policy)
- `internal/controller/toolroute_reconciler_test.go` — envtest specs
- `internal/controller/toolroute_authz.go` — glob-to-CEL translation for `AgentgatewayPolicy.mcpAuthorization.rules`
- `internal/controller/toolroute_authz_test.go` — unit tests for the translator
- `config/samples/toolgateway_v1alpha1_toolroute.yaml` — sample
- `test/e2e/toolroute_e2e_test.go` — e2e

**Modified:**
- `go.mod` + `go.sum` — replace directive to local agent-runtime-operator during dev; bump to published version before release
- `cmd/main.go` — unregister `ToolServerReconciler`, register `ToolRouteReconciler`
- `internal/controller/suite_test.go` — register the ToolRoute CRD in envtest
- `README.md` — update architecture, remove stale ToolServer-driven flow
- `CLAUDE.md` — update project structure and architecture

**Deleted:**
- `internal/controller/toolserver_reconciler.go`
- `internal/controller/toolserver_reconciler_test.go`

---

## Task 1: Dev-time dependency pin on agent-runtime-operator

The new `ToolRoute` type and the stripped `ToolServer` are not yet in any published release of `agent-runtime-operator`. Add a `replace` directive to consume the local checkout. Remove it before release.

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add the `replace` directive**

Append to the bottom of `go.mod`:

```
replace github.com/agentic-layer/agent-runtime-operator => ../agent-runtime-operator
```

- [ ] **Step 2: Tidy modules and verify build**

Run:
```bash
go mod tidy && go build ./...
```

Expected: build succeeds and `go.sum` updates. The new `runtimev1alpha1.ToolRoute` type is now available in this repo's imports.

- [ ] **Step 3: Verify ToolServer no longer has `ToolGatewayRef` fields**

Run:
```bash
go vet ./... 2>&1 | head -30
```

Expected: compilation errors in `internal/controller/toolserver_reconciler.go` because `ToolServer.Status.ToolGatewayRef` no longer exists. Those errors disappear in Task 4 when the file is deleted. Do NOT commit yet.

---

## Task 2: Register ToolRoute CRD in envtest

**Files:**
- Modify: `internal/controller/suite_test.go`

- [ ] **Step 1: Add the ToolRoute CRD path to envtest config**

Open `suite_test.go` and locate the `envtest.Environment` setup (it lists CRD YAML paths for agent-runtime-operator CRDs already). Add the path to the new `toolroutes.yaml`:

```go
CRDDirectoryPaths: []string{
    // existing entries, e.g.:
    filepath.Join("..", "..", "..", "agent-runtime-operator", "config", "crd", "bases"),
    // (no change needed here if the existing entry already points at the whole bases/ dir -- new ToolRoute CRD file is automatically picked up)
},
```

If the existing setup uses individual file paths rather than the bases directory, add:

```go
filepath.Join("..", "..", "..", "agent-runtime-operator", "config", "crd", "bases", "runtime.agentic-layer.ai_toolroutes.yaml"),
```

- [ ] **Step 2: No independent commit yet**

This change only compiles meaningfully once the reconciler exists (Task 3). Hold off on committing until Task 3 commits together.

---

## Task 3: Scaffold `ToolRouteReconciler` (without filter policy)

Implements: class ownership filter, upstream resolution, `AgentgatewayBackend` + `HTTPRoute` creation, `status.url` population. `AgentgatewayPolicy` for tool filtering is handled in Task 4.

**Files:**
- Create: `internal/controller/toolroute_reconciler.go`
- Create: `internal/controller/toolroute_reconciler_test.go`

Pattern reference: `toolgateway_reconciler.go` for class-filtering (`shouldProcessToolGateway`), `toolserver_reconciler.go` for `CreateOrPatch` pattern on `AgentgatewayBackend` + `HTTPRoute`. Reuse helpers in `resources.go` (`newAgentgatewayBackend`, `buildMCPTarget`, `setMCPTargets`, `buildHTTPRouteSpec`, `toolServerHost`).

- [ ] **Step 1: Write the failing test — reconciler skips a ToolRoute targeting a non-agentgateway class**

In `toolroute_reconciler_test.go`:

```go
package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	runtimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

var _ = Describe("ToolRouteReconciler", func() {
	ctx := context.Background()
	const ns = "default"

	It("skips a ToolRoute whose ToolGateway class is not owned by this controller", func() {
		// ToolGatewayClass owned by someone else
		otherClass := &runtimev1alpha1.ToolGatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "other-class"},
			Spec:       runtimev1alpha1.ToolGatewayClassSpec{Controller: "example.com/other"},
		}
		Expect(k8sClient.Create(ctx, otherClass)).To(Succeed())

		tg := &runtimev1alpha1.ToolGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "foreign-tg", Namespace: ns},
			Spec:       runtimev1alpha1.ToolGatewaySpec{ToolGatewayClassName: "other-class"},
		}
		Expect(k8sClient.Create(ctx, tg)).To(Succeed())

		tr := &runtimev1alpha1.ToolRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "tr-skip", Namespace: ns},
			Spec: runtimev1alpha1.ToolRouteSpec{
				ToolGatewayRef: corev1.ObjectReference{Name: "foreign-tg"},
				Upstream: runtimev1alpha1.ToolRouteUpstream{
					External: &runtimev1alpha1.ExternalUpstream{Url: "https://example.com/mcp"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())

		r := newToolRouteReconciler()
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: tr.Name, Namespace: tr.Namespace}})
		Expect(err).NotTo(HaveOccurred())

		// Backend should NOT be created
		backend := &unstructured.Unstructured{}
		backend.SetAPIVersion("agentgateway.dev/v1alpha1")
		backend.SetKind("AgentgatewayBackend")
		err = k8sClient.Get(ctx, types.NamespacedName{Name: "foreign-tg-tr-skip", Namespace: ns}, backend)
		Expect(err).To(HaveOccurred()) // IsNotFound
	})
})

// newToolRouteReconciler is a test helper; implement alongside the reconciler in step 4.
```

- [ ] **Step 2: Run the test, expect it to fail at compile**

Run:
```bash
go test ./internal/controller/ -v -ginkgo.focus="ToolRouteReconciler" 2>&1 | head -30
```

Expected: compile failure — `newToolRouteReconciler` undefined.

- [ ] **Step 3: Add more failing tests before writing the reconciler**

Append to `toolroute_reconciler_test.go`:

```go
It("creates an AgentgatewayBackend and HTTPRoute for a cluster ToolServer upstream", func() {
	ownClass := &runtimev1alpha1.ToolGatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "agentgateway"},
		Spec:       runtimev1alpha1.ToolGatewayClassSpec{Controller: ToolGatewayAgentgatewayControllerName},
	}
	Expect(k8sClient.Create(ctx, ownClass)).To(Succeed())

	tg := &runtimev1alpha1.ToolGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "own-tg", Namespace: ns},
		Spec:       runtimev1alpha1.ToolGatewaySpec{ToolGatewayClassName: "agentgateway"},
	}
	Expect(k8sClient.Create(ctx, tg)).To(Succeed())

	ts := &runtimev1alpha1.ToolServer{
		ObjectMeta: metav1.ObjectMeta{Name: "ts-1", Namespace: ns},
		Spec: runtimev1alpha1.ToolServerSpec{
			Protocol:      "mcp",
			TransportType: "http",
			Image:         "example/mcp:latest",
			Port:          8080,
			Path:          "/mcp",
		},
	}
	Expect(k8sClient.Create(ctx, ts)).To(Succeed())

	tr := &runtimev1alpha1.ToolRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "tr-cluster", Namespace: ns},
		Spec: runtimev1alpha1.ToolRouteSpec{
			ToolGatewayRef: corev1.ObjectReference{Name: "own-tg"},
			Upstream: runtimev1alpha1.ToolRouteUpstream{
				ToolServerRef: &corev1.ObjectReference{Name: "ts-1"},
			},
		},
	}
	Expect(k8sClient.Create(ctx, tr)).To(Succeed())

	r := newToolRouteReconciler()
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "tr-cluster", Namespace: ns}})
	Expect(err).NotTo(HaveOccurred())

	// AgentgatewayBackend exists with host pointing at ts-1's Service
	backend := &unstructured.Unstructured{}
	backend.SetAPIVersion("agentgateway.dev/v1alpha1")
	backend.SetKind("AgentgatewayBackend")
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "own-tg-tr-cluster", Namespace: ns}, backend)).To(Succeed())
	targets, found, err := unstructured.NestedSlice(backend.Object, "spec", "mcp", "targets")
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(targets).To(HaveLen(1))
	target := targets[0].(map[string]interface{})
	static := target["static"].(map[string]interface{})
	Expect(static["host"]).To(Equal("ts-1.default.svc.cluster.local"))

	// HTTPRoute exists attached to own-tg
	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("gateway.networking.k8s.io/v1")
	hr.SetKind("HTTPRoute")
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "own-tg-tr-cluster", Namespace: ns}, hr)).To(Succeed())

	// status.url is populated to the gateway path
	updated := &runtimev1alpha1.ToolRoute{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "tr-cluster", Namespace: ns}, updated)).To(Succeed())
	Expect(updated.Status.Url).To(ContainSubstring("/default/tr-cluster/mcp"))
})

It("creates a backend with a static host for an external upstream", func() {
	// assume own-tg and ownClass from the previous test already exist
	tr := &runtimev1alpha1.ToolRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "tr-external", Namespace: ns},
		Spec: runtimev1alpha1.ToolRouteSpec{
			ToolGatewayRef: corev1.ObjectReference{Name: "own-tg"},
			Upstream: runtimev1alpha1.ToolRouteUpstream{
				External: &runtimev1alpha1.ExternalUpstream{Url: "https://github-mcp.example.com/mcp"},
			},
		},
	}
	Expect(k8sClient.Create(ctx, tr)).To(Succeed())

	r := newToolRouteReconciler()
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "tr-external", Namespace: ns}})
	Expect(err).NotTo(HaveOccurred())

	backend := &unstructured.Unstructured{}
	backend.SetAPIVersion("agentgateway.dev/v1alpha1")
	backend.SetKind("AgentgatewayBackend")
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "own-tg-tr-external", Namespace: ns}, backend)).To(Succeed())
	targets, _, _ := unstructured.NestedSlice(backend.Object, "spec", "mcp", "targets")
	target := targets[0].(map[string]interface{})
	static := target["static"].(map[string]interface{})
	Expect(static["host"]).To(Equal("github-mcp.example.com"))
	Expect(static["port"]).To(Equal(int64(443)))
	Expect(static["path"]).To(Equal("/mcp"))
})
```

- [ ] **Step 4: Implement `toolroute_reconciler.go`**

Create `internal/controller/toolroute_reconciler.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	runtimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
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
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

func (r *ToolRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var route runtimev1alpha1.ToolRoute
	if err := r.Get(ctx, req.NamespacedName, &route); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	gatewayNs := route.Spec.ToolGatewayRef.Namespace
	if gatewayNs == "" {
		gatewayNs = route.Namespace
	}

	var toolGateway runtimev1alpha1.ToolGateway
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

// shouldProcess checks whether this controller owns the referenced ToolGateway via its class.
func (r *ToolRouteReconciler) shouldProcess(ctx context.Context, tg *runtimev1alpha1.ToolGateway) bool {
	// Mirrors ToolGatewayReconciler.shouldProcessToolGateway.
	var classList runtimev1alpha1.ToolGatewayClassList
	if err := r.List(ctx, &classList); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list ToolGatewayClasses")
		return false
	}
	var owned []runtimev1alpha1.ToolGatewayClass
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
	// Unclassed ToolGateways are only claimed if exactly one class is owned by this controller.
	return len(owned) == 1
}

// resolveUpstream returns the host, port, and path for the MCP target built from upstream.
func (r *ToolRouteReconciler) resolveUpstream(ctx context.Context, route *runtimev1alpha1.ToolRoute) (host string, port int32, path string, err error) {
	switch {
	case route.Spec.Upstream.ToolServerRef != nil:
		ref := route.Spec.Upstream.ToolServerRef
		ns := ref.Namespace
		if ns == "" {
			ns = route.Namespace
		}
		var ts runtimev1alpha1.ToolServer
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
		h := u.Hostname()
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
			p = int32(n)
		}
		path := u.Path
		if path == "" {
			path = "/"
		}
		return h, p, path, nil

	default:
		return "", 0, "", fmt.Errorf("upstream has neither toolServerRef nor external set")
	}
}

func (r *ToolRouteReconciler) ensureBackend(ctx context.Context, route *runtimev1alpha1.ToolRoute, tg *runtimev1alpha1.ToolGateway, host string, port int32, path string) error {
	backend := newAgentgatewayBackend(tg.Name+"-"+route.Name, route.Namespace)
	_, err := controllerutil.CreateOrPatch(ctx, r.Client, backend, func() error {
		if e := controllerutil.SetControllerReference(route, backend, r.Scheme); e != nil {
			return e
		}
		target := buildMCPTarget("mcp-target", host, port, path)
		return setMCPTargets(backend, []interface{}{target})
	})
	return err
}

// ensurePolicy creates or updates the AgentgatewayPolicy carrying mcpAuthorization
// CEL rules derived from route.spec.toolFilter. If toolFilter is nil, the policy
// is deleted (tools pass through unfiltered). Implemented in Task 4.
func (r *ToolRouteReconciler) ensurePolicy(ctx context.Context, route *runtimev1alpha1.ToolRoute, tg *runtimev1alpha1.ToolGateway) error {
	// Stub — filled in by Task 4.
	return nil
}

func (r *ToolRouteReconciler) ensureHTTPRoute(ctx context.Context, route *runtimev1alpha1.ToolRoute, tg *runtimev1alpha1.ToolGateway, routePath string) error {
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
	return err
}

func (r *ToolRouteReconciler) updateStatus(ctx context.Context, route *runtimev1alpha1.ToolRoute, tg *runtimev1alpha1.ToolGateway, routePath string) (ctrl.Result, error) {
	// URL shape mirrors current per-ToolServer HTTPRoute paths: http://<gateway-svc>/<routePath>.
	// Gateway service URL is derived by convention (same as today's ToolGateway service naming).
	gwURL := fmt.Sprintf("http://%s.%s.svc.cluster.local%s", tg.Name, tg.Namespace, routePath)
	route.Status.Url = gwURL
	metav1.SetMetaDataAnnotation(&route.ObjectMeta, "runtime.agentic-layer.ai/observed-generation", fmt.Sprintf("%d", route.Generation))
	if err := r.Status().Update(ctx, route); err != nil {
		return ctrl.Result{}, err
	}
	r.Recorder.Eventf(route, nil, "Normal", "Ready", "Ready", "ToolRoute ready at %s", gwURL)
	return ctrl.Result{}, nil
}

// newToolRouteReconciler is a test helper that returns a reconciler wired to the envtest client.
func newToolRouteReconciler() *ToolRouteReconciler {
	return &ToolRouteReconciler{
		Client:   k8sClient,
		Scheme:   k8sClient.Scheme(),
		Recorder: noopRecorder{},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ToolRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&runtimev1alpha1.ToolRoute{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Named(ToolRouteAgentgatewayControllerName).
		Complete(r)
}
```

Note: `ensurePolicy` is introduced as a stub in this file; its real body lives in Task 4 (`toolroute_authz.go`). Task 3's tests exercise only Backend + HTTPRoute + status; policy behavior is exercised in Task 4.

And `noopRecorder` in the test suite already (check `test_utils.go`) — if missing, define one in the same file:

```go
type noopRecorder struct{}

func (noopRecorder) Eventf(_ runtime.Object, _ *corev1.ObjectReference, _, _, _, _ string, _ ...interface{}) {
}
// Add the other kevents.EventRecorder methods as no-ops.
```

- [ ] **Step 5: Update `suite_test.go` to register the ToolRoute CRD**

See Task 2 — merge those changes now.

- [ ] **Step 6: Run the tests**

Run:
```bash
go test ./internal/controller/ -v -ginkgo.focus="ToolRouteReconciler"
```

Expected: three specs pass (skip-foreign-class, cluster-ToolServer upstream, external upstream).

- [ ] **Step 7: Commit**

Do NOT commit yet — Task 4 also touches `toolroute_filter.go` (the stub introduced here becomes the real implementation). Commit at the end of Task 4.

---

## Task 4: Build `AgentgatewayPolicy` with CEL tool-filter rules

**Research summary (already done, recorded here for context):** Agentgateway has no allow/deny field on `AgentgatewayBackend`. The canonical tool-filtering mechanism is `AgentgatewayPolicy.spec.backend.mcpAuthorization.rules` — a list of CEL expressions evaluated per request against `mcp.tool.name`. A tool is visible (in `tools/list`) and callable iff at least one rule evaluates to true. The policy attaches to an `HTTPRoute` via `targetRefs`. Supported CEL string functions relevant for globs: `startsWith`, `endsWith`, `contains`, `matches` (regex). Source: https://agentgateway.dev/docs/kubernetes/latest/mcp/tool-access and https://agentgateway.dev/docs/kubernetes/latest/reference/cel.

**Translation contract:** glob allow/deny from our CRD → a single CEL rule of the form `(allowExpr) && !(denyExpr)`:
- `allowExpr` is the OR of allow patterns, each compiled to a CEL predicate on `mcp.tool.name`. If the allow list is empty, `allowExpr == true`.
- `denyExpr` is the OR of deny patterns, likewise compiled. If empty, the `&& !(denyExpr)` clause is omitted.
- A glob like `get_*` compiles to the regex `^get_.*$` via `mcp.tool.name.matches("^get_.*$")`. Plain names compile to `==`. `*` → `.*`, `?` → `.` (standard glob to regex, with regex metacharacters escaped).

When `toolFilter` is nil, no `AgentgatewayPolicy` is created; if one existed from a previous reconcile, it's deleted.

**Files:**
- Create: `internal/controller/toolroute_authz.go`
- Create: `internal/controller/toolroute_authz_test.go`
- Modify: `internal/controller/toolroute_reconciler.go` (real body for `ensurePolicy`)
- Modify: `internal/controller/toolroute_reconciler_test.go` (add policy assertions)

- [ ] **Step 1: Write glob-to-CEL unit tests**

Create `internal/controller/toolroute_authz_test.go`:

```go
package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	runtimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

var _ = Describe("globToCELPredicate", func() {
	It("compiles a plain name to an equality check", func() {
		Expect(globToCELPredicate("search_issues")).
			To(Equal(`mcp.tool.name == "search_issues"`))
	})

	It("compiles a glob with trailing * to a regex matches()", func() {
		Expect(globToCELPredicate("get_*")).
			To(Equal(`mcp.tool.name.matches("^get_.*$")`))
	})

	It("compiles a glob with surrounding * to a regex matches()", func() {
		Expect(globToCELPredicate("*delete*")).
			To(Equal(`mcp.tool.name.matches("^.*delete.*$")`))
	})

	It("treats ? as a single-char wildcard", func() {
		Expect(globToCELPredicate("do?")).
			To(Equal(`mcp.tool.name.matches("^do.$")`))
	})

	It("escapes regex metacharacters in a plain-looking name", func() {
		Expect(globToCELPredicate("a.b")).
			To(Equal(`mcp.tool.name == "a.b"`)) // no glob metas -> equality path
		Expect(globToCELPredicate("a.b*")).
			To(Equal(`mcp.tool.name.matches("^a\\.b.*$")`))
	})
})

var _ = Describe("buildMcpAuthorizationRules", func() {
	It("returns a single true-rule when filter is nil", func() {
		// We never actually emit a policy when filter is nil; the reconciler handles that.
		// But the builder is still exercised:
		Expect(buildMcpAuthorizationRules(nil)).To(BeEmpty())
	})

	It("returns a single rule ORing allow patterns when only allow is set", func() {
		rules := buildMcpAuthorizationRules(&runtimev1alpha1.ToolFilter{
			Allow: []string{"get_*", "list_*"},
		})
		Expect(rules).To(ConsistOf(
			`mcp.tool.name.matches("^get_.*$") || mcp.tool.name.matches("^list_.*$")`,
		))
	})

	It("returns a rule negating deny patterns when only deny is set", func() {
		rules := buildMcpAuthorizationRules(&runtimev1alpha1.ToolFilter{
			Deny: []string{"*delete*"},
		})
		Expect(rules).To(ConsistOf(
			`!(mcp.tool.name.matches("^.*delete.*$"))`,
		))
	})

	It("ANDs allow and (negated) deny when both are set", func() {
		rules := buildMcpAuthorizationRules(&runtimev1alpha1.ToolFilter{
			Allow: []string{"get_*"},
			Deny:  []string{"force_push"},
		})
		Expect(rules).To(ConsistOf(
			`(mcp.tool.name.matches("^get_.*$")) && !(mcp.tool.name == "force_push")`,
		))
	})
})
```

- [ ] **Step 2: Implement `toolroute_authz.go`**

Create `internal/controller/toolroute_authz.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package-local tool-filter translation: our CRD expresses filtering as glob allow/deny.
// agentgateway expresses it as CEL rules in AgentgatewayPolicy.spec.backend.mcpAuthorization.rules.
// This file builds those CEL rules.
package controller

import (
	"regexp"
	"strings"

	runtimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

// hasGlobMeta returns true if the pattern contains '*' or '?'.
func hasGlobMeta(p string) bool {
	return strings.ContainsAny(p, "*?")
}

// globToRegex converts a glob (* and ?) into an anchored regex. Regex metacharacters
// in the rest of the pattern are escaped via regexp.QuoteMeta on non-wildcard segments.
func globToRegex(glob string) string {
	var b strings.Builder
	b.WriteString("^")
	// Split on wildcards while keeping them. We walk char-by-char to preserve order.
	var literal strings.Builder
	flushLiteral := func() {
		if literal.Len() > 0 {
			b.WriteString(regexp.QuoteMeta(literal.String()))
			literal.Reset()
		}
	}
	for _, r := range glob {
		switch r {
		case '*':
			flushLiteral()
			b.WriteString(".*")
		case '?':
			flushLiteral()
			b.WriteString(".")
		default:
			literal.WriteRune(r)
		}
	}
	flushLiteral()
	b.WriteString("$")
	return b.String()
}

// cELEscape double-quotes a string as a CEL string literal, escaping backslashes and quotes.
func cELEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// globToCELPredicate compiles a single glob pattern to a CEL boolean expression over mcp.tool.name.
// Plain names (no glob metas) compile to an equality check; patterns with * or ? use matches().
func globToCELPredicate(pattern string) string {
	if !hasGlobMeta(pattern) {
		return "mcp.tool.name == " + cELEscape(pattern)
	}
	return "mcp.tool.name.matches(" + cELEscape(globToRegex(pattern)) + ")"
}

// orJoin joins a list of CEL predicates with `||`.
func orJoin(preds []string) string {
	return strings.Join(preds, " || ")
}

// buildMcpAuthorizationRules converts a ToolFilter into the list of CEL rules to write
// into AgentgatewayPolicy.spec.backend.mcpAuthorization.rules.
// Returns nil when filter is nil (policy not needed) or when allow and deny are both empty.
func buildMcpAuthorizationRules(filter *runtimev1alpha1.ToolFilter) []string {
	if filter == nil || (len(filter.Allow) == 0 && len(filter.Deny) == 0) {
		return nil
	}

	var allowPreds []string
	for _, p := range filter.Allow {
		allowPreds = append(allowPreds, globToCELPredicate(p))
	}
	var denyPreds []string
	for _, p := range filter.Deny {
		denyPreds = append(denyPreds, globToCELPredicate(p))
	}

	switch {
	case len(allowPreds) > 0 && len(denyPreds) > 0:
		return []string{"(" + orJoin(allowPreds) + ") && !(" + orJoin(denyPreds) + ")"}
	case len(allowPreds) > 0:
		return []string{orJoin(allowPreds)}
	case len(denyPreds) > 0:
		return []string{"!(" + orJoin(denyPreds) + ")"}
	default:
		return nil
	}
}
```

- [ ] **Step 3: Run the translator tests**

```bash
go test ./internal/controller/ -v -ginkgo.focus="globToCELPredicate|buildMcpAuthorizationRules"
```

Expected: all specs pass.

- [ ] **Step 4: Implement `ensurePolicy` in the reconciler**

Replace the stub `ensurePolicy` method in `toolroute_reconciler.go` with the real implementation. Reuse the existing `newAgentgatewayPolicy` helper in `resources.go` (already used by the guardrails feature):

```go
func (r *ToolRouteReconciler) ensurePolicy(ctx context.Context, route *runtimev1alpha1.ToolRoute, tg *runtimev1alpha1.ToolGateway) error {
	policyName := tg.Name + "-" + route.Name + "-toolfilter"

	rules := buildMcpAuthorizationRules(route.Spec.ToolFilter)

	// No filter configured — ensure any previously-created policy is gone.
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

		// spec.targetRefs: [ { group: gateway.networking.k8s.io, kind: HTTPRoute, name: <tg>-<route> } ]
		targetRef := map[string]interface{}{
			"group": "gateway.networking.k8s.io",
			"kind":  "HTTPRoute",
			"name":  tg.Name + "-" + route.Name,
		}
		if e := unstructured.SetNestedSlice(policy.Object, []interface{}{targetRef}, "spec", "targetRefs"); e != nil {
			return e
		}

		// spec.backend.mcpAuthorization.rules: [ "<cel>" ]
		ruleSlice := make([]interface{}, len(rules))
		for i, s := range rules {
			ruleSlice[i] = s
		}
		if e := unstructured.SetNestedSlice(policy.Object, ruleSlice, "spec", "backend", "mcpAuthorization", "rules"); e != nil {
			return e
		}
		return nil
	})
	return err
}
```

You'll need to add `"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"` to the imports of `toolroute_reconciler.go` if it isn't already there, and ensure `apierrors` is imported.

- [ ] **Step 5: Add policy assertions to the reconciler tests**

In `toolroute_reconciler_test.go`, append specs after the existing ones:

```go
It("creates an AgentgatewayPolicy with CEL rules when toolFilter is set", func() {
	// assumes own-tg + ownClass already exist from prior specs in the same Describe
	tr := &runtimev1alpha1.ToolRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "tr-filtered", Namespace: ns},
		Spec: runtimev1alpha1.ToolRouteSpec{
			ToolGatewayRef: corev1.ObjectReference{Name: "own-tg"},
			Upstream: runtimev1alpha1.ToolRouteUpstream{
				External: &runtimev1alpha1.ExternalUpstream{Url: "https://example.com/mcp"},
			},
			ToolFilter: &runtimev1alpha1.ToolFilter{
				Allow: []string{"get_*"},
				Deny:  []string{"force_push"},
			},
		},
	}
	Expect(k8sClient.Create(ctx, tr)).To(Succeed())

	r := newToolRouteReconciler()
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "tr-filtered", Namespace: ns}})
	Expect(err).NotTo(HaveOccurred())

	policy := &unstructured.Unstructured{}
	policy.SetAPIVersion("agentgateway.dev/v1alpha1")
	policy.SetKind("AgentgatewayPolicy")
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "own-tg-tr-filtered-toolfilter", Namespace: ns}, policy)).To(Succeed())

	rules, found, err := unstructured.NestedStringSlice(policy.Object, "spec", "backend", "mcpAuthorization", "rules")
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	Expect(rules).To(ConsistOf(`(mcp.tool.name.matches("^get_.*$")) && !(mcp.tool.name == "force_push")`))

	targetRefs, _, _ := unstructured.NestedSlice(policy.Object, "spec", "targetRefs")
	Expect(targetRefs).To(HaveLen(1))
	Expect(targetRefs[0].(map[string]interface{})["kind"]).To(Equal("HTTPRoute"))
	Expect(targetRefs[0].(map[string]interface{})["name"]).To(Equal("own-tg-tr-filtered"))
})

It("deletes the policy when toolFilter is cleared", func() {
	// Continuation of previous spec: clear ToolFilter and re-reconcile; policy should be gone.
	tr := &runtimev1alpha1.ToolRoute{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "tr-filtered", Namespace: ns}, tr)).To(Succeed())
	tr.Spec.ToolFilter = nil
	Expect(k8sClient.Update(ctx, tr)).To(Succeed())

	r := newToolRouteReconciler()
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "tr-filtered", Namespace: ns}})
	Expect(err).NotTo(HaveOccurred())

	policy := &unstructured.Unstructured{}
	policy.SetAPIVersion("agentgateway.dev/v1alpha1")
	policy.SetKind("AgentgatewayPolicy")
	err = k8sClient.Get(ctx, types.NamespacedName{Name: "own-tg-tr-filtered-toolfilter", Namespace: ns}, policy)
	Expect(apierrors.IsNotFound(err)).To(BeTrue())
})
```

- [ ] **Step 6: Run the full controller suite**

```bash
make test
```

Expected: every spec passes (existing ToolGatewayReconciler specs + new ToolRouteReconciler + authz translator + policy assertions).

---

## Task 5: Delete `ToolServerReconciler` and wire `ToolRouteReconciler` in `main.go`

**Files:**
- Delete: `internal/controller/toolserver_reconciler.go`
- Delete: `internal/controller/toolserver_reconciler_test.go`
- Modify: `cmd/main.go`

- [ ] **Step 1: Unregister `ToolServerReconciler` and register `ToolRouteReconciler` in `main.go`**

Open `cmd/main.go`. Locate the block that instantiates and calls `SetupWithManager` for `controller.ToolServerReconciler{...}`. Replace it with the equivalent for `ToolRouteReconciler`:

```go
if err := (&controller.ToolRouteReconciler{
    Client:   mgr.GetClient(),
    Scheme:   mgr.GetScheme(),
    Recorder: mgr.GetEventRecorderFor("toolroute-controller"),
}).SetupWithManager(mgr); err != nil {
    setupLog.Error(err, "unable to create controller", "controller", "ToolRoute")
    os.Exit(1)
}
```

Remove any `import` of the ToolServer-reconciler-specific symbols if they were hoisted up (should just be the struct type which is in the same package).

- [ ] **Step 2: Delete the old reconciler files**

```bash
rm internal/controller/toolserver_reconciler.go \
   internal/controller/toolserver_reconciler_test.go
```

- [ ] **Step 3: Verify build**

Run:
```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 4: Run the full suite**

```bash
make test
```

Expected: green.

- [ ] **Step 5: Commit Tasks 1–5 together**

These tasks cannot commit independently because they span a breaking upstream schema change (Task 1) through to the final wiring. Commit the combined change:

```bash
  git add go.mod go.sum \
          internal/controller/suite_test.go \
          internal/controller/toolroute_reconciler.go \
          internal/controller/toolroute_reconciler_test.go \
          internal/controller/toolroute_authz.go \
          internal/controller/toolroute_authz_test.go \
          cmd/main.go && \
  git rm internal/controller/toolserver_reconciler.go \
         internal/controller/toolserver_reconciler_test.go && \
  git commit -m "feat!: reconcile ToolRoute instead of ToolServer.toolGatewayRef

Adds ToolRouteReconciler that builds AgentgatewayBackend + HTTPRoute per
ToolRoute and — when toolFilter is set — an AgentgatewayPolicy with
mcpAuthorization CEL rules translated from the ToolRoute's glob allow/deny.
Populates ToolRoute.status.url. Removes the legacy ToolServerReconciler
that keyed off the now-removed ToolServer.status.toolGatewayRef."
```

---

## Task 6: Sample manifest and docs updates

**Files:**
- Create: `config/samples/toolgateway_v1alpha1_toolroute.yaml`
- Modify: `config/samples/toolgateway_v1alpha1_toolgateway_with_toolserver.yaml` (remove the stale `toolGatewayRef` field from the ToolServer; add a companion ToolRoute if the file is meant as a complete working example)
- Modify: `README.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Create a new ToolRoute sample**

Write `config/samples/toolgateway_v1alpha1_toolroute.yaml`:

```yaml
---
apiVersion: runtime.agentic-layer.ai/v1alpha1
kind: ToolGateway
metadata:
  name: my-tool-gateway
  namespace: default
spec:
  toolGatewayClassName: agentgateway
---
apiVersion: runtime.agentic-layer.ai/v1alpha1
kind: ToolServer
metadata:
  name: my-tool-server
  namespace: default
spec:
  protocol: mcp
  transportType: http
  image: my-tool-server:latest
  port: 8000
  path: /mcp
  replicas: 1
---
apiVersion: runtime.agentic-layer.ai/v1alpha1
kind: ToolRoute
metadata:
  name: my-tool-route
  namespace: default
spec:
  toolGatewayRef:
    name: my-tool-gateway
  upstream:
    toolServerRef:
      name: my-tool-server
  toolFilter:
    allow: ["get_*", "list_*"]
    deny:  ["*delete*"]
```

- [ ] **Step 2: Update `toolgateway_v1alpha1_toolgateway_with_toolserver.yaml`**

Open and strip any `toolGatewayRef` block from the ToolServer body. If the file is intended as a complete working example, append a ToolRoute that references the ToolGateway + ToolServer it declares. If the file was only ever a compile-style sample, just remove the now-invalid field.

- [ ] **Step 3: Update `README.md`**

In the "How it Works" section, replace the per-ToolServer flow description with a per-ToolRoute flow. Replace the architecture block:

```
ToolGateway (CRD)
    |
Gateway (same name and namespace)

ToolRoute (CRD)
    |
HTTPRoute (Gateway API) --> AgentgatewayBackend --> upstream (ToolServer or external URL)
```

Update the ToolServer Configuration section to note that exposing a ToolServer now requires a companion `ToolRoute`. Add a short subsection "ToolRoute Configuration" showing the sample above.

- [ ] **Step 4: Update `CLAUDE.md`**

In the Project Structure block, remove any `toolserver_reconciler.go` reference and add `toolroute_reconciler.go` and `toolroute_filter.go`.

In the Architecture block, add a subsection describing the ToolRoute reconciliation flow (upstream resolution, class ownership, filter translation, status.url population).

- [ ] **Step 5: Commit**

```bash
  git add config/samples/toolgateway_v1alpha1_toolroute.yaml \
          config/samples/toolgateway_v1alpha1_toolgateway_with_toolserver.yaml \
          README.md CLAUDE.md && \
  git commit -m "docs: ToolRoute sample and architecture updates"
```

---

## Task 7: E2E test for ToolRoute reconciliation

**Files:**
- Create: `test/e2e/toolroute_e2e_test.go`

Pattern reference: `test/e2e/toolgateway_e2e_test.go` for bootstrap, and `e2e_suite_test.go` for shared helpers.

- [ ] **Step 1: Write the e2e test**

Create the test file with three specs:

1. Apply ToolGatewayClass (controller = `ToolGatewayAgentgatewayControllerName`) → ToolGateway → ToolServer → ToolRoute. Verify within 60 seconds:
   - An `AgentgatewayBackend` named `<tg>-<route>` exists in the route's namespace and its MCP target `static.host` equals `<ts>.<ns>.svc.cluster.local`.
   - An `HTTPRoute` named `<tg>-<route>` exists with parent ref `<tg>` and path match `/<ns>/<route>/mcp`.
   - `ToolRoute.status.url` is populated and ends with `/<ns>/<route>/mcp`.
2. Apply a ToolRoute with `upstream.external.url: https://example.com/mcp`. Verify the backend's MCP target has `static.host: example.com`, port `443`, path `/mcp`.
3. Apply a ToolRoute targeting a ToolGateway whose class is NOT owned by this controller. Verify no backend or HTTPRoute is created within 30 seconds.

Use the same test helpers (`ensureNamespace`, `applyYAML`, `waitForResource`, `k8sClient`) already present in `e2e_suite_test.go`. Skeleton:

```go
package e2e

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	runtimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

var _ = Describe("ToolRoute E2E", Ordered, func() {
	const ns = "e2e-toolroute"

	BeforeAll(func() { ensureNamespace(ns) })
	AfterAll(func() { deleteNamespace(ns) })

	It("creates backend + route + status for a cluster ToolServer upstream", func() {
		// class + gateway
		Expect(k8sClient.Create(ctx, &runtimev1alpha1.ToolGatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "agentgateway"},
			Spec:       runtimev1alpha1.ToolGatewayClassSpec{Controller: "runtime.agentic-layer.ai/tool-gateway-agentgateway-controller"},
		})).To(Or(Succeed(), SatisfyAny(/* already-exists OK */)))
		Expect(k8sClient.Create(ctx, &runtimev1alpha1.ToolGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "tg", Namespace: ns},
			Spec:       runtimev1alpha1.ToolGatewaySpec{ToolGatewayClassName: "agentgateway"},
		})).To(Succeed())

		// toolserver + toolroute
		Expect(k8sClient.Create(ctx, &runtimev1alpha1.ToolServer{
			ObjectMeta: metav1.ObjectMeta{Name: "ts", Namespace: ns},
			Spec: runtimev1alpha1.ToolServerSpec{
				Protocol: "mcp", TransportType: "http",
				Image: "example/mcp:latest", Port: 8080, Path: "/mcp",
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &runtimev1alpha1.ToolRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "tr", Namespace: ns},
			Spec: runtimev1alpha1.ToolRouteSpec{
				ToolGatewayRef: corev1.ObjectReference{Name: "tg"},
				Upstream: runtimev1alpha1.ToolRouteUpstream{
					ToolServerRef: &corev1.ObjectReference{Name: "ts"},
				},
			},
		})).To(Succeed())

		// assertions
		Eventually(func(g Gomega) {
			backend := &unstructured.Unstructured{}
			backend.SetAPIVersion("agentgateway.dev/v1alpha1")
			backend.SetKind("AgentgatewayBackend")
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "tg-tr", Namespace: ns}, backend)).To(Succeed())
			targets, _, _ := unstructured.NestedSlice(backend.Object, "spec", "mcp", "targets")
			g.Expect(targets).To(HaveLen(1))
			g.Expect(targets[0].(map[string]interface{})["static"].(map[string]interface{})["host"]).To(Equal(fmt.Sprintf("ts.%s.svc.cluster.local", ns)))

			tr := &runtimev1alpha1.ToolRoute{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "tr", Namespace: ns}, tr)).To(Succeed())
			g.Expect(tr.Status.Url).To(HaveSuffix(fmt.Sprintf("/%s/tr/mcp", ns)))
		}, 60*time.Second, 2*time.Second).Should(Succeed())
	})

	// analogous It() blocks for the external upstream and foreign-class cases
})
```

- [ ] **Step 2: Run e2e**

```bash
make test-e2e
```

Expected: all specs pass.

- [ ] **Step 3: Commit**

```bash
  git add test/e2e/toolroute_e2e_test.go && \
  git commit -m "test(e2e): verify ToolRouteReconciler wiring and status"
```

---

## Final verification

- [ ] **Step 1: Full test matrix**

```bash
make lint && make test && make test-e2e
```

Expected: green across lint, unit/integration, and e2e.

- [ ] **Step 2: Before release — pin a published agent-runtime-operator version**

When the agent-runtime-operator plan cuts a release tag:

1. Edit `go.mod`: bump `github.com/agentic-layer/agent-runtime-operator v0.27.1` to the new version that includes `ToolRoute` and the stripped `ToolServer`.
2. Remove the `replace` directive added in Task 1.
3. `go mod tidy` and `make test`.
4. Commit: `chore: bump agent-runtime-operator to vX.Y.Z (ToolRoute)`.
