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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

var _ = Describe("GuardReconciler", func() {
	var (
		ns           string
		providerName = "presidio-analyzer"
		guardName    = "pii-guard"
	)

	BeforeEach(func() {
		ns = fmt.Sprintf("guard-test-%d", time.Now().UnixNano())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})).To(Succeed())
	})

	createProvider := func() *agentruntimev1alpha1.GuardrailProvider {
		p := &agentruntimev1alpha1.GuardrailProvider{
			ObjectMeta: metav1.ObjectMeta{Name: providerName, Namespace: ns},
			Spec: agentruntimev1alpha1.GuardrailProviderSpec{
				Type: "presidio-api",
				Presidio: &agentruntimev1alpha1.PresidioProviderConfig{
					BaseUrl: "http://presidio.svc:8080",
				},
			},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		return p
	}

	createGuard := func() *agentruntimev1alpha1.Guard {
		g := &agentruntimev1alpha1.Guard{
			ObjectMeta: metav1.ObjectMeta{Name: guardName, Namespace: ns},
			Spec: agentruntimev1alpha1.GuardSpec{
				Mode:        []agentruntimev1alpha1.GuardMode{agentruntimev1alpha1.GuardModePreCall},
				ProviderRef: corev1.ObjectReference{Name: providerName},
				Presidio: &agentruntimev1alpha1.PresidioGuardConfig{
					Language:        "en",
					EntityActions:   map[string]string{"PERSON": "MASK"},
					ScoreThresholds: map[string]string{"ALL": "0.5"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, g)).To(Succeed())
		return g
	}

	It("renders a ConfigMap whose data matches the static-config schema", func() {
		createProvider()
		createGuard()

		var cm corev1.ConfigMap
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      guardName + "-adapter",
				Namespace: ns,
			}, &cm)
		}, "5s", "100ms").Should(Succeed())

		yaml := cm.Data[staticConfigKey]
		Expect(yaml).To(ContainSubstring("provider: presidio-api"))
		Expect(yaml).To(ContainSubstring("- pre_call"))
		Expect(yaml).To(ContainSubstring("endpoint: http://presidio.svc:8080"))
		Expect(yaml).To(ContainSubstring("PERSON: MASK"))

		// Owner ref points back at the Guard
		Expect(cm.OwnerReferences).To(HaveLen(1))
		Expect(cm.OwnerReferences[0].Kind).To(Equal("Guard"))
		Expect(cm.OwnerReferences[0].Name).To(Equal(guardName))

		// Guard becomes Ready=True
		Eventually(func(g Gomega) {
			var guard agentruntimev1alpha1.Guard
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: guardName, Namespace: ns}, &guard)).To(Succeed())
			cond := metaConditionReason(guard.Status.Conditions)
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(cond.Reason).To(Equal(guardReasonReconciled))
		}, "5s", "100ms").Should(Succeed())
	})

	It("creates a Deployment with the operator's adapter image and the config mounted", func() {
		createProvider()
		createGuard()

		var dep appsv1.Deployment
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      guardName + "-adapter",
				Namespace: ns,
			}, &dep)
		}, "5s", "100ms").Should(Succeed())

		Expect(dep.OwnerReferences).To(HaveLen(1))
		Expect(dep.OwnerReferences[0].Name).To(Equal(guardName))
		Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
		c := dep.Spec.Template.Spec.Containers[0]
		Expect(c.Image).To(Equal("ghcr.io/agentic-layer/guardrail-adapter:test"))
		Expect(c.Args).To(ContainElements(
			fmt.Sprintf("--addr=:%d", adapterContainerPort),
			fmt.Sprintf("--health-addr=:%d", adapterHealthPort),
		))
		Expect(c.Env).To(ContainElement(corev1.EnvVar{
			Name: "GUARDRAIL_CONFIG_FILE", Value: "/etc/guardrail/config.yaml",
		}))
		Expect(c.VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name:      "guardrail-config",
			MountPath: "/etc/guardrail",
			ReadOnly:  true,
		}))
		Expect(dep.Spec.Template.Spec.Volumes).To(ContainElement(HaveField("Name", "guardrail-config")))

		Expect(dep.Spec.Template.Annotations).To(HaveKey(configHashAnnotation))
		Expect(dep.Spec.Template.Annotations[configHashAnnotation]).NotTo(BeEmpty())
	})

	It("re-renders the ConfigMap and rolls the Deployment when the provider's baseUrl changes", func() {
		provider := createProvider()
		createGuard()

		var depBefore appsv1.Deployment
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: guardName + "-adapter", Namespace: ns}, &depBefore)
		}, "5s", "100ms").Should(Succeed())
		hashBefore := depBefore.Spec.Template.Annotations[configHashAnnotation]
		Expect(hashBefore).NotTo(BeEmpty())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: providerName, Namespace: ns}, provider)).To(Succeed())
		provider.Spec.Presidio.BaseUrl = "http://presidio-v2.svc:8080"
		Expect(k8sClient.Update(ctx, provider)).To(Succeed())

		Eventually(func() string {
			var dep appsv1.Deployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: guardName + "-adapter", Namespace: ns}, &dep); err != nil {
				return ""
			}
			return dep.Spec.Template.Annotations[configHashAnnotation]
		}, "5s", "100ms").ShouldNot(Equal(hashBefore))
	})

	It("creates a Service exposing port 80 → containerPort 9001 with h2c appProtocol", func() {
		createProvider()
		createGuard()

		var svc corev1.Service
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      guardName + "-adapter",
				Namespace: ns,
			}, &svc)
		}, "5s", "100ms").Should(Succeed())

		Expect(svc.OwnerReferences).To(HaveLen(1))
		Expect(svc.OwnerReferences[0].Name).To(Equal(guardName))
		Expect(svc.Spec.Ports).To(HaveLen(1))
		Expect(svc.Spec.Ports[0].Port).To(Equal(int32(AdapterServicePort)))
		Expect(svc.Spec.Ports[0].TargetPort.IntValue()).To(Equal(adapterContainerPort))
		Expect(svc.Spec.Ports[0].AppProtocol).NotTo(BeNil())
		Expect(*svc.Spec.Ports[0].AppProtocol).To(Equal("kubernetes.io/h2c"))
		Expect(svc.Spec.Selector).To(HaveKeyWithValue("app", guardName+"-adapter"))
	})

	It("sets Ready=False/ProviderNotFound when the providerRef does not resolve", func() {
		g := &agentruntimev1alpha1.Guard{
			ObjectMeta: metav1.ObjectMeta{Name: guardName, Namespace: ns},
			Spec: agentruntimev1alpha1.GuardSpec{
				Mode:        []agentruntimev1alpha1.GuardMode{agentruntimev1alpha1.GuardModePreCall},
				ProviderRef: corev1.ObjectReference{Name: "missing-provider"},
			},
		}
		Expect(k8sClient.Create(ctx, g)).To(Succeed())

		Eventually(func(gomega Gomega) {
			var guard agentruntimev1alpha1.Guard
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: guardName, Namespace: ns}, &guard)).To(Succeed())
			cond := metaConditionReason(guard.Status.Conditions)
			gomega.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			gomega.Expect(cond.Reason).To(Equal(guardReasonProviderNotFound))
		}, "5s", "100ms").Should(Succeed())

		var cm corev1.ConfigMap
		err := k8sClient.Get(ctx, types.NamespacedName{Name: guardName + "-adapter", Namespace: ns}, &cm)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("sets Ready=False/UnsupportedProviderType for non-presidio providers", func() {
		p := &agentruntimev1alpha1.GuardrailProvider{
			ObjectMeta: metav1.ObjectMeta{Name: providerName, Namespace: ns},
			Spec: agentruntimev1alpha1.GuardrailProviderSpec{
				Type: "openai-moderation-api",
			},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		createGuard()

		Eventually(func(gomega Gomega) {
			var guard agentruntimev1alpha1.Guard
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: guardName, Namespace: ns}, &guard)).To(Succeed())
			cond := metaConditionReason(guard.Status.Conditions)
			gomega.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			gomega.Expect(cond.Reason).To(Equal(guardReasonUnsupportedProviderType))
		}, "5s", "100ms").Should(Succeed())
	})

	It("sets Ready=False/AdapterNotConfigured when --guardrail-adapter-image is empty", func() {
		original := guardReconciler.AdapterImage
		guardReconciler.AdapterImage = ""
		defer func() { guardReconciler.AdapterImage = original }()

		createProvider()
		createGuard()

		Eventually(func(gomega Gomega) {
			var guard agentruntimev1alpha1.Guard
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: guardName, Namespace: ns}, &guard)).To(Succeed())
			cond := metaConditionReason(guard.Status.Conditions)
			gomega.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			gomega.Expect(cond.Reason).To(Equal(guardReasonAdapterNotConfigured))
		}, "5s", "100ms").Should(Succeed())
	})

	AfterEach(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		}))).To(Succeed())
	})
})

// metaConditionReason returns the Ready condition or an empty struct.
func metaConditionReason(conds []metav1.Condition) metav1.Condition {
	for _, c := range conds {
		if c.Type == guardReadyType {
			return c
		}
	}
	return metav1.Condition{}
}
