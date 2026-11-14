package kibo

import "testing"

// The real core declares the key pin and rejects an unknown provider before it touches the binding.
func TestFFIAppCoreCapabilitiesAndProvider(t *testing.T) {
	var core FFIAppCore
	found := false
	for _, c := range core.CoreCapabilities() {
		if c == "expected_key_refs" {
			found = true
		}
	}
	if !found {
		t.Fatalf("core_capabilities() lacks expected_key_refs: %v", core.CoreCapabilities())
	}
	if _, err := ffiProvider("nope"); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if _, err := ffiPosture("nope"); err == nil {
		t.Fatal("unknown posture accepted")
	}
}
