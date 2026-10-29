package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubotdev/kubox/internal/slice"
)

func runInit(t *testing.T, dir string, args ...string) error {
	t.Helper()
	cmd := newInitCmd()
	cmd.SetArgs(args)
	cmd.SetOut(nil)
	cmd.SetErr(nil)
	return cmd.Execute()
}

func TestInitScaffoldIsValidSlice(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "payments.kubox")
	if err := runInit(t, dir, dir); err != nil {
		t.Fatalf("init: %v", err)
	}

	s, sliceDir, err := slice.Load(dir)
	if err != nil {
		t.Fatalf("scaffolded slice does not load: %v", err)
	}
	if sliceDir != dir {
		t.Errorf("slice dir = %q, want %q", sliceDir, dir)
	}
	if s.Name != "payments-kubox" {
		t.Errorf("Name = %q, want payments-kubox (sanitized from dir)", s.Name)
	}
	if s.App.Port != 8080 {
		t.Errorf("App.Port = %d, want 8080", s.App.Port)
	}
	if s.App.HealthCheck == nil || s.App.HealthCheck.Path != "/healthz" {
		t.Errorf("App.HealthCheck = %+v, want path /healthz", s.App.HealthCheck)
	}

	script := filepath.Join(dir, "tests", "load.js")
	b, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read scaffolded script: %v", err)
	}
	if !strings.Contains(string(b), "__ENV.BASE_URL") {
		t.Error("scaffolded load.js does not reference __ENV.BASE_URL")
	}
}

func TestInitNameFlagOverridesDirName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "whatever")
	if err := runInit(t, dir, dir, "--name", "checkout"); err != nil {
		t.Fatalf("init: %v", err)
	}
	s, _, err := slice.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.Name != "checkout" {
		t.Errorf("Name = %q, want checkout", s.Name)
	}
}

func TestInitRefusesOverwriteWithoutForce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app")
	if err := runInit(t, dir, dir); err != nil {
		t.Fatalf("first init: %v", err)
	}
	if err := runInit(t, dir, dir); err == nil {
		t.Error("second init without --force: expected error")
	}
	if err := runInit(t, dir, dir, "--force"); err != nil {
		t.Errorf("init --force: %v", err)
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"myapp", "myapp"},
		{"My.App_Kubox", "my-app-kubox"},
		{"---weird!!name---", "weird-name"},
		{"UPPER", "upper"},
		{"", "app"},
		{"!!!", "app"},
		{strings.Repeat("a", 80), strings.Repeat("a", 63)},
		{strings.Repeat("a", 63) + "-tail", strings.Repeat("a", 63)},
	}
	for _, tt := range tests {
		if got := sanitizeName(tt.in); got != tt.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
