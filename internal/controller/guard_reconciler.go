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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

const (
	GuardAdapterControllerName = "runtime.agentic-layer.ai/guard-adapter-controller"

	// AdapterServicePort is the cluster port exposed by the per-Guard adapter Service.
	// It maps to containerPort 9001 inside the adapter pod.
	AdapterServicePort = 80

	// adapterContainerPort is the port the adapter listens on for ext_proc.
	adapterContainerPort = 9001

	// adapterHealthPort is the HTTP health endpoint.
	adapterHealthPort = 8080

	// configHashAnnotation is set on the Deployment's pod template so it rolls
	// when the ConfigMap content changes.
	configHashAnnotation = "runtime.agentic-layer.ai/config-hash"

	guardReadyType = "Ready"

	// Guard Ready=False reasons
	guardReasonReconciled              = "Reconciled"
	guardReasonAdapterNotConfigured    = "AdapterNotConfigured"
	guardReasonProviderNotFound        = "ProviderNotFound"
	guardReasonUnsupportedProviderType = "UnsupportedProviderType"
	guardReasonInvalidConfig           = "InvalidConfig"
	guardReasonReconcileFailed         = "ReconcileFailed"
)

// GuardReconciler reconciles a Guard object into its adapter triple
// (Deployment + ConfigMap + Service) in the Guard's own namespace.
type GuardReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Recorder     kevents.EventRecorder
	AdapterImage string // operator flag --guardrail-adapter-image
}

// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=guards,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=guards/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=runtime.agentic-layer.ai,resources=guardrailproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *GuardReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var guard agentruntimev1alpha1.Guard
	if err := r.Get(ctx, req.NamespacedName, &guard); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get Guard")
		return ctrl.Result{}, err
	}

	reason, msg, reconcileErr := r.reconcileGuard(ctx, &guard)
	if reconcileErr != nil {
		if statusErr := r.setReadyCondition(ctx, &guard, metav1.ConditionFalse, reason, msg); statusErr != nil {
			log.Error(statusErr, "Failed to update Guard status to NotReady")
		}
		if isTerminalReason(reason) {
			// Watches on Guard and GuardrailProvider re-trigger reconciliation
			// when inputs are corrected; backoff retries would only add noise.
			log.Info("Reconciliation paused pending user input", "reason", reason, "message", msg)
			return ctrl.Result{}, nil
		}
		log.Error(reconcileErr, "Reconciliation failed", "reason", reason)
		return ctrl.Result{}, reconcileErr
	}

	if err := r.setReadyCondition(ctx, &guard, metav1.ConditionTrue, guardReasonReconciled, "Adapter resources reconciled"); err != nil {
		log.Error(err, "Failed to update Guard status to Ready")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// isTerminalReason returns true for Ready=False reasons that cannot progress
// without an edit to the Guard, the GuardrailProvider, or operator flags.
func isTerminalReason(reason string) bool {
	switch reason {
	case guardReasonAdapterNotConfigured,
		guardReasonProviderNotFound,
		guardReasonUnsupportedProviderType,
		guardReasonInvalidConfig:
		return true
	}
	return false
}

func (r *GuardReconciler) reconcileGuard(ctx context.Context, guard *agentruntimev1alpha1.Guard) (string, string, error) {
	if r.AdapterImage == "" {
		return guardReasonAdapterNotConfigured,
			"--guardrail-adapter-image flag is not set on the operator",
			fmt.Errorf("adapter image not configured")
	}

	provider, err := r.fetchProvider(ctx, guard)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return guardReasonProviderNotFound, err.Error(), err
		}
		return guardReasonReconcileFailed, err.Error(), err
	}

	configYAML, configHash, err := renderStaticConfig(guard, provider)
	if err != nil {
		switch {
		case errors.Is(err, errUnsupportedProvider):
			return guardReasonUnsupportedProviderType, err.Error(), err
		case errors.Is(err, errInvalidConfig):
			return guardReasonInvalidConfig, err.Error(), err
		default:
			return guardReasonReconcileFailed, err.Error(), err
		}
	}

	if err := r.ensureConfigMap(ctx, guard, configYAML); err != nil {
		return guardReasonReconcileFailed, err.Error(), err
	}

	// configHash is consumed by the Deployment template in Task 2.5.
	_ = configHash
	return "", "", nil
}

// adapterName returns the shared name for the per-Guard adapter triple.
func adapterName(guard *agentruntimev1alpha1.Guard) string {
	return guard.Name + "-adapter"
}

func (r *GuardReconciler) ensureConfigMap(ctx context.Context, guard *agentruntimev1alpha1.Guard, configYAML []byte) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      adapterName(guard),
			Namespace: guard.Namespace,
		},
	}
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, cm, func() error {
		if err := controllerutil.SetControllerReference(guard, cm, r.Scheme); err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[staticConfigKey] = string(configYAML)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to ensure ConfigMap: %w", err)
	}
	logf.FromContext(ctx).Info("ConfigMap reconciled", "operation", op, "name", cm.Name)
	return nil
}

func (r *GuardReconciler) fetchProvider(ctx context.Context, guard *agentruntimev1alpha1.Guard) (*agentruntimev1alpha1.GuardrailProvider, error) {
	ns := guard.Spec.ProviderRef.Namespace
	if ns == "" {
		ns = guard.Namespace
	}
	var provider agentruntimev1alpha1.GuardrailProvider
	key := types.NamespacedName{Name: guard.Spec.ProviderRef.Name, Namespace: ns}
	if err := r.Get(ctx, key, &provider); err != nil {
		return nil, err
	}
	return &provider, nil
}

func (r *GuardReconciler) setReadyCondition(ctx context.Context, guard *agentruntimev1alpha1.Guard, status metav1.ConditionStatus, reason, message string) error {
	patch := client.MergeFrom(guard.DeepCopy())
	apimeta.SetStatusCondition(&guard.Status.Conditions, metav1.Condition{
		Type:               guardReadyType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: guard.Generation,
	})
	if err := r.Status().Patch(ctx, guard, patch); err != nil {
		return fmt.Errorf("failed to patch Guard status: %w", err)
	}
	return nil
}

// SetupWithManager registers the reconciler.
func (r *GuardReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentruntimev1alpha1.Guard{}).
		Named(GuardAdapterControllerName).
		Complete(r)
}
