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
	agentGatewayClassName = "agentgateway"
	readyConditionType    = "Ready"
	listenerNameHTTP      = "http"
)

// Condition reasons for ToolGateway Ready conditions. Kept fine-grained so
// `kubectl get` / `describe` / watchers can differentiate which phase of
// reconciliation failed without parsing the message.
const (
	reasonReconciled                   = "Reconciled"
	reasonAgentgatewayParametersFailed = "AgentgatewayParametersReconciliationFailed"
	reasonGatewayFailed                = "GatewayReconciliationFailed"
	reasonGuardrailsFailed             = "GuardrailsReconciliationFailed"
	reasonGuardNotFound                = "GuardNotFound"
	reasonGuardNotReady                = "GuardNotReady"
)

// Sentinels classifying guardrail-related errors so reconcileToolGateway can
// pick the right Ready=False reason.
var (
	errGuardNotFound = errors.New("guard not found")
	errGuardNotReady = errors.New("guard not ready")
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
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=guards,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewayparameters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agentgateway.dev,resources=agentgatewaypolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ToolGatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

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

	if err := r.ensureGuardrails(ctx, toolGateway); err != nil {
		switch {
		case errors.Is(err, errGuardNotFound):
			return reasonGuardNotFound, err
		case errors.Is(err, errGuardNotReady):
			return reasonGuardNotReady, err
		default:
			return reasonGuardrailsFailed, err
		}
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
					Name:     listenerNameHTTP,
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
	agentgatewayPolicy := newAgentgatewayPolicy("", "")
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentruntimev1alpha1.ToolGateway{}).
		Owns(&gatewayv1.Gateway{}).
		Owns(agentgatewayParams).
		Owns(agentgatewayPolicy).
		Watches(
			&agentruntimev1alpha1.Guard{},
			handler.EnqueueRequestsFromMapFunc(r.findToolGatewayForGuard),
		).
		Watches(
			&agentruntimev1alpha1.GuardrailProvider{},
			handler.EnqueueRequestsFromMapFunc(r.findToolGatewayForGuardrailProvider),
		).
		Named(ToolGatewayAgentgatewayControllerName).
		Complete(r)
}

// findToolGatewayForGuard maps a Guard to all ToolGateways that reference it
func (r *ToolGatewayReconciler) findToolGatewayForGuard(ctx context.Context, obj client.Object) []ctrl.Request {
	guard, ok := obj.(*agentruntimev1alpha1.Guard)
	if !ok {
		return nil
	}

	// List all ToolGateways
	var toolGatewayList agentruntimev1alpha1.ToolGatewayList
	if err := r.List(ctx, &toolGatewayList); err != nil {
		return nil
	}

	// Find ToolGateways that reference this Guard
	var requests []ctrl.Request
	for _, tg := range toolGatewayList.Items {
		for _, guardRef := range tg.Spec.Guardrails {
			guardNamespace := guardRef.Namespace
			if guardNamespace == "" {
				guardNamespace = tg.Namespace
			}
			if guardRef.Name == guard.Name && guardNamespace == guard.Namespace {
				requests = append(requests, ctrl.Request{
					NamespacedName: client.ObjectKey{
						Name:      tg.Name,
						Namespace: tg.Namespace,
					},
				})
				break
			}
		}
	}

	return requests
}

// findToolGatewayForGuardrailProvider maps a GuardrailProvider to all ToolGateways
// that reference Guards which use this provider
func (r *ToolGatewayReconciler) findToolGatewayForGuardrailProvider(ctx context.Context, obj client.Object) []ctrl.Request {
	provider, ok := obj.(*agentruntimev1alpha1.GuardrailProvider)
	if !ok {
		return nil
	}

	// List all Guards
	var guardList agentruntimev1alpha1.GuardList
	if err := r.List(ctx, &guardList); err != nil {
		return nil
	}

	// Find Guards that reference this provider
	affectedGuards := make(map[string]bool)
	for _, guard := range guardList.Items {
		providerNamespace := guard.Spec.ProviderRef.Namespace
		if providerNamespace == "" {
			providerNamespace = guard.Namespace
		}
		if guard.Spec.ProviderRef.Name == provider.Name && providerNamespace == provider.Namespace {
			affectedGuards[guard.Namespace+"/"+guard.Name] = true
		}
	}

	// List all ToolGateways
	var toolGatewayList agentruntimev1alpha1.ToolGatewayList
	if err := r.List(ctx, &toolGatewayList); err != nil {
		return nil
	}

	// Find ToolGateways that reference affected Guards
	var requests []ctrl.Request
	for _, tg := range toolGatewayList.Items {
		for _, guardRef := range tg.Spec.Guardrails {
			guardNamespace := guardRef.Namespace
			if guardNamespace == "" {
				guardNamespace = tg.Namespace
			}
			guardKey := guardNamespace + "/" + guardRef.Name
			if affectedGuards[guardKey] {
				requests = append(requests, ctrl.Request{
					NamespacedName: client.ObjectKey{
						Name:      tg.Name,
						Namespace: tg.Namespace,
					},
				})
				break
			}
		}
	}

	return requests
}

// ensureGuardrails creates, updates, or deletes the AgentgatewayPolicy for guardrails.
// The policy points the ext_proc backendRef at the per-Guard adapter Service
// reconciled by GuardReconciler in the Guard's own namespace; no metadataContext
// is needed because the adapter loads its config from a ConfigMap.
func (r *ToolGatewayReconciler) ensureGuardrails(ctx context.Context, toolGateway *agentruntimev1alpha1.ToolGateway) error {
	log := logf.FromContext(ctx)

	policyName := toolGateway.Name + "-guardrail"
	policy := newAgentgatewayPolicy(policyName, toolGateway.Namespace)

	// 1. No guardrails configured: ensure no policy exists.
	if len(toolGateway.Spec.Guardrails) == 0 {
		if err := r.Delete(ctx, policy); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete guardrail policy: %w", err)
		}
		apimeta.RemoveStatusCondition(&toolGateway.Status.Conditions, "GuardrailsUnsupported")
		log.Info("Deleted guardrail policy (no guardrails configured)")
		return nil
	}

	// 2. Multi-guard: not supported by agentgateway (one ext_proc slot per target).
	if len(toolGateway.Spec.Guardrails) > 1 {
		apimeta.SetStatusCondition(&toolGateway.Status.Conditions, metav1.Condition{
			Type:               "GuardrailsUnsupported",
			Status:             metav1.ConditionTrue,
			Reason:             "MultipleGuardsNotSupported",
			Message:            "Multiple guards are not supported (agentgateway has only one ext_proc slot per target)",
			ObservedGeneration: toolGateway.Generation,
		})
		return fmt.Errorf("multiple guards not supported")
	}

	// 3. Resolve the referenced Guard.
	ref := toolGateway.Spec.Guardrails[0]
	guardNS := ref.Namespace
	if guardNS == "" {
		guardNS = toolGateway.Namespace
	}
	var guard agentruntimev1alpha1.Guard
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: guardNS}, &guard); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: %s/%s", errGuardNotFound, guardNS, ref.Name)
		}
		return fmt.Errorf("failed to get guard %s/%s: %w", guardNS, ref.Name, err)
	}

	// 4. Surface Guard's own Ready condition. We require Guard=Ready before
	//    wiring the policy so traffic isn't routed to a half-configured adapter.
	if cond := apimeta.FindStatusCondition(guard.Status.Conditions, "Ready"); cond == nil || cond.Status != metav1.ConditionTrue {
		if cond != nil {
			return fmt.Errorf("%w: %s/%s: %s — %s", errGuardNotReady, guardNS, ref.Name, cond.Reason, cond.Message)
		}
		return fmt.Errorf("%w: %s/%s has no Ready condition yet", errGuardNotReady, guardNS, ref.Name)
	}

	// 5. Build and apply the policy.
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, policy, func() error {
		if err := controllerutil.SetControllerReference(toolGateway, policy, r.Scheme); err != nil {
			return err
		}
		spec := buildGuardrailPolicySpec(toolGateway.Name, ref.Name, guardNS)
		return unstructured.SetNestedMap(policy.Object, spec, "spec")
	})
	if err != nil {
		return fmt.Errorf("failed to create or update guardrail policy: %w", err)
	}

	switch op {
	case controllerutil.OperationResultCreated:
		r.emitNormalEvent(toolGateway, "GuardrailPolicyCreated", "CreateGuardrailPolicy",
			"Created guardrail policy %s", policy.GetName())
	case controllerutil.OperationResultUpdated:
		r.emitNormalEvent(toolGateway, "GuardrailPolicyUpdated", "UpdateGuardrailPolicy",
			"Updated guardrail policy %s", policy.GetName())
	}
	apimeta.RemoveStatusCondition(&toolGateway.Status.Conditions, "GuardrailsUnsupported")
	log.Info("Guardrail policy reconciled", "operation", op, "name", policy.GetName())
	return nil
}

// buildGuardrailPolicySpec builds the AgentgatewayPolicy spec map. The ext_proc
// backendRef points at the per-Guard adapter Service ("<guard>-adapter") in the
// Guard's own namespace; failureMode is FailClosed so traffic is blocked when
// the adapter is unreachable.
func buildGuardrailPolicySpec(toolGatewayName, guardName, guardNamespace string) map[string]interface{} {
	return map[string]interface{}{
		"targetRefs": []interface{}{
			map[string]interface{}{
				"group": "gateway.networking.k8s.io",
				"kind":  "Gateway",
				"name":  toolGatewayName,
			},
		},
		"traffic": map[string]interface{}{
			"extProc": map[string]interface{}{
				"backendRef": map[string]interface{}{
					"name":      guardName + "-adapter",
					"namespace": guardNamespace,
					"port":      int64(AdapterServicePort),
				},
				"failureMode": "FailClosed",
			},
		},
	}
}
