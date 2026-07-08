package observability

import (
	"context"
	"testing"
)

func TestEnabledUsesConfigDefaultWhenEnvUnset(t *testing.T) {
	t.Setenv(EnvVar, "")
	if Enabled(false) {
		t.Fatal("Enabled(false) = true, want false")
	}
	if !Enabled(true) {
		t.Fatal("Enabled(true) = false, want true")
	}
}

func TestEnabledEnvOverridesConfigDefault(t *testing.T) {
	t.Setenv(EnvVar, "0")
	if Enabled(true) {
		t.Fatal("Enabled(true) = true, want false from env override")
	}
	t.Setenv(EnvVar, "on")
	if !Enabled(false) {
		t.Fatal("Enabled(false) = false, want true from env override")
	}
}

func TestConfigureDisabledReturnsNoopShutdown(t *testing.T) {
	t.Setenv(EnvVar, "off")
	shutdown, err := Configure(context.Background(), Options{Enabled: true})
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if shutdown == nil {
		t.Fatal("Configure() returned nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown() error = %v", err)
	}
}
