package latencytrace

import "testing"

func TestEnabledUsesConfigWhenEnvUnset(t *testing.T) {
	t.Setenv(EnvVar, "")
	SetConfigEnabled(false)
	if Enabled() {
		t.Fatal("Enabled() = true, want false from config")
	}
	SetConfigEnabled(true)
	if !Enabled() {
		t.Fatal("Enabled() = false, want true from config")
	}
	SetConfigEnabled(false)
}

func TestEnabledEnvOverridesConfig(t *testing.T) {
	SetConfigEnabled(true)
	t.Setenv(EnvVar, "0")
	if Enabled() {
		t.Fatal("Enabled() = true, want false from env override")
	}
	SetConfigEnabled(false)
	t.Setenv(EnvVar, "1")
	if !Enabled() {
		t.Fatal("Enabled() = false, want true from env override")
	}
	SetConfigEnabled(false)
}

func TestCommandShapeRedactsFlagValues(t *testing.T) {
	got := CommandShape([]string{"issue", "update", "cji", "--notes", "secret body"})
	if got != "issue update cji" {
		t.Fatalf("CommandShape() = %q, want issue update cji", got)
	}
	got = CommandShape([]string{"config", "set", "diagnostics.latencyTrace", "true"})
	if got != "config set diagnostics.latencyTrace" {
		t.Fatalf("CommandShape() = %q, want config set diagnostics.latencyTrace", got)
	}
}
