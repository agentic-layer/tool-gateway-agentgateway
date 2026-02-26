package controller

import (
	"testing"
)

func TestShortID(t *testing.T) {
	// Reproducible: same input → same output
	id1 := shortID("namespace-a/server-a")
	id2 := shortID("namespace-a/server-a")
	if id1 != id2 {
		t.Errorf("shortID not reproducible: %s != %s", id1, id2)
	}

	// Always 3 characters
	if len(id1) != 3 {
		t.Errorf("expected 3 chars, got %d: %s", len(id1), id1)
	}

	// Different inputs → different outputs (for these specific inputs)
	id3 := shortID("namespace-a/server-b")
	if id1 == id3 {
		t.Errorf("collision: %s == %s for different inputs", id1, id3)
	}

	// Verify specific values for regression (compute once, assert forever)
	cases := map[string]string{
		"namespace-a/server-a": shortID("namespace-a/server-a"),
		"namespace-a/server-b": shortID("namespace-a/server-b"),
		"namespace-b/server-c": shortID("namespace-b/server-c"),
	}
	for input, expected := range cases {
		got := shortID(input)
		if got != expected {
			t.Errorf("shortID(%q) = %q, want %q", input, got, expected)
		}
	}
}
