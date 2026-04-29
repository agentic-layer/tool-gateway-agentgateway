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
	corev1 "k8s.io/api/core/v1"
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
			cond := metaConditionReason(guard.Status.Conditions, guardReadyType)
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(cond.Reason).To(Equal(guardReasonReconciled))
		}, "5s", "100ms").Should(Succeed())
	})

	AfterEach(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		}))).To(Succeed())
	})
})

// metaConditionReason returns the named condition or an empty struct.
func metaConditionReason(conds []metav1.Condition, t string) metav1.Condition {
	for _, c := range conds {
		if c.Type == t {
			return c
		}
	}
	return metav1.Condition{}
}
