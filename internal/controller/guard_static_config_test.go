package controller

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentruntimev1alpha1 "github.com/agentic-layer/agent-runtime-operator/api/v1alpha1"
)

func TestRenderStaticConfig_PresidioFull(t *testing.T) {
	guard := &agentruntimev1alpha1.Guard{
		ObjectMeta: metav1.ObjectMeta{Name: "pii-guard", Namespace: "default"},
		Spec: agentruntimev1alpha1.GuardSpec{
			Mode: []agentruntimev1alpha1.GuardMode{
				agentruntimev1alpha1.GuardModePreCall,
				agentruntimev1alpha1.GuardModePostCall,
			},
			ProviderRef: corev1.ObjectReference{Name: "presidio-analyzer"},
			Presidio: &agentruntimev1alpha1.PresidioGuardConfig{
				Language: "en",
				ScoreThresholds: map[string]string{
					"PERSON": "0.8",
					"ALL":    "0.5",
				},
				EntityActions: map[string]string{
					"PERSON":        "MASK",
					"EMAIL_ADDRESS": "MASK",
				},
			},
		},
	}
	provider := &agentruntimev1alpha1.GuardrailProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "presidio-analyzer", Namespace: "default"},
		Spec: agentruntimev1alpha1.GuardrailProviderSpec{
			Type: "presidio-api",
			Presidio: &agentruntimev1alpha1.PresidioProviderConfig{
				BaseUrl: "http://presidio-analyzer.default.svc:8080",
			},
		},
	}

	data, hash, err := renderStaticConfig(guard, provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}

	got := string(data)
	if !contains(got, "provider: presidio-api") {
		t.Errorf("missing provider line, got:\n%s", got)
	}
	if !contains(got, "- pre_call") || !contains(got, "- post_call") {
		t.Errorf("missing modes, got:\n%s", got)
	}
	if !contains(got, "endpoint: http://presidio-analyzer.default.svc:8080") {
		t.Errorf("missing endpoint, got:\n%s", got)
	}
	if !contains(got, "PERSON: MASK") {
		t.Errorf("missing entity action, got:\n%s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRenderStaticConfig_Deterministic(t *testing.T) {
	mk := func(seed int) (*agentruntimev1alpha1.Guard, *agentruntimev1alpha1.GuardrailProvider) {
		// Same content, but built map-by-map in different orders.
		actions := map[string]string{}
		thresholds := map[string]string{}
		switch seed % 2 {
		case 0:
			actions["PERSON"] = "MASK"
			actions["EMAIL_ADDRESS"] = "MASK"
			thresholds["ALL"] = "0.5"
			thresholds["PERSON"] = "0.8"
		case 1:
			actions["EMAIL_ADDRESS"] = "MASK"
			actions["PERSON"] = "MASK"
			thresholds["PERSON"] = "0.8"
			thresholds["ALL"] = "0.5"
		}
		return &agentruntimev1alpha1.Guard{
				ObjectMeta: metav1.ObjectMeta{Name: "g"},
				Spec: agentruntimev1alpha1.GuardSpec{
					Mode: []agentruntimev1alpha1.GuardMode{agentruntimev1alpha1.GuardModePreCall},
					Presidio: &agentruntimev1alpha1.PresidioGuardConfig{
						ScoreThresholds: thresholds,
						EntityActions:   actions,
					},
				},
			}, &agentruntimev1alpha1.GuardrailProvider{
				ObjectMeta: metav1.ObjectMeta{Name: "p"},
				Spec: agentruntimev1alpha1.GuardrailProviderSpec{
					Type: "presidio-api",
					Presidio: &agentruntimev1alpha1.PresidioProviderConfig{
						BaseUrl: "http://p.default.svc:8080",
					},
				},
			}
	}

	g1, p1 := mk(0)
	g2, p2 := mk(1)
	d1, h1, err := renderStaticConfig(g1, p1)
	if err != nil {
		t.Fatal(err)
	}
	d2, h2, err := renderStaticConfig(g2, p2)
	if err != nil {
		t.Fatal(err)
	}
	if string(d1) != string(d2) {
		t.Errorf("rendering not deterministic across map insertion orders\nd1=%q\nd2=%q", d1, d2)
	}
	if h1 != h2 {
		t.Errorf("hash differs: %s vs %s", h1, h2)
	}
}

func TestRenderStaticConfig_Errors(t *testing.T) {
	baseProvider := func() *agentruntimev1alpha1.GuardrailProvider {
		return &agentruntimev1alpha1.GuardrailProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
			Spec: agentruntimev1alpha1.GuardrailProviderSpec{
				Type: "presidio-api",
				Presidio: &agentruntimev1alpha1.PresidioProviderConfig{
					BaseUrl: "http://presidio.default.svc:8080",
				},
			},
		}
	}
	baseGuard := func() *agentruntimev1alpha1.Guard {
		return &agentruntimev1alpha1.Guard{
			ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "default"},
			Spec: agentruntimev1alpha1.GuardSpec{
				Mode: []agentruntimev1alpha1.GuardMode{agentruntimev1alpha1.GuardModePreCall},
			},
		}
	}

	cases := []struct {
		name    string
		mutate  func(*agentruntimev1alpha1.Guard, *agentruntimev1alpha1.GuardrailProvider)
		wantErr error
	}{
		{
			name:    "openai is unsupported",
			mutate:  func(_ *agentruntimev1alpha1.Guard, p *agentruntimev1alpha1.GuardrailProvider) { p.Spec.Type = "openai-moderation-api" },
			wantErr: errUnsupportedProvider,
		},
		{
			name:    "bedrock is unsupported",
			mutate:  func(_ *agentruntimev1alpha1.Guard, p *agentruntimev1alpha1.GuardrailProvider) { p.Spec.Type = "bedrock-api" },
			wantErr: errUnsupportedProvider,
		},
		{
			name:    "empty modes",
			mutate:  func(g *agentruntimev1alpha1.Guard, _ *agentruntimev1alpha1.GuardrailProvider) { g.Spec.Mode = nil },
			wantErr: errInvalidConfig,
		},
		{
			name: "during_call is not accepted by adapter",
			mutate: func(g *agentruntimev1alpha1.Guard, _ *agentruntimev1alpha1.GuardrailProvider) {
				g.Spec.Mode = []agentruntimev1alpha1.GuardMode{agentruntimev1alpha1.GuardModeDuringCall}
			},
			wantErr: errInvalidConfig,
		},
		{
			name:    "missing presidio provider config",
			mutate:  func(_ *agentruntimev1alpha1.Guard, p *agentruntimev1alpha1.GuardrailProvider) { p.Spec.Presidio = nil },
			wantErr: errInvalidConfig,
		},
		{
			name:    "empty presidio baseUrl",
			mutate:  func(_ *agentruntimev1alpha1.Guard, p *agentruntimev1alpha1.GuardrailProvider) { p.Spec.Presidio.BaseUrl = "" },
			wantErr: errInvalidConfig,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, p := baseGuard(), baseProvider()
			tc.mutate(g, p)
			_, _, err := renderStaticConfig(g, p)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got err=%v, want errors.Is(%v)", err, tc.wantErr)
			}
		})
	}
}
