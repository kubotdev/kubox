package slice

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAndValidate(t *testing.T) {
	dir := t.TempDir()
	yaml := `
name: payment
app:
  image: myregistry/payment:latest
  port: 8080
  env:
    INVENTORY_URL: http://inventory:8080
dependencies:
  - name: inventory
    image: myregistry/inventory:latest
    port: 8080
  - name: postgres
    image: postgres:16-alpine
    port: 5432
    env:
      POSTGRES_PASSWORD: kubox
quotas:
  cpu: "2"
  memory: 4Gi
  pods: "20"
`
	if err := os.WriteFile(filepath.Join(dir, "kubox.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	s, gotDir, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotDir != dir {
		t.Errorf("dir = %q, want %q", gotDir, dir)
	}
	if s.Name != "payment" {
		t.Errorf("Name = %q, want payment", s.Name)
	}
	if s.App.Port != 8080 {
		t.Errorf("App.Port = %d, want 8080", s.App.Port)
	}
	if len(s.Dependencies) != 2 {
		t.Errorf("Dependencies = %d, want 2", len(s.Dependencies))
	}
	if s.Dependencies[1].Image != "postgres:16-alpine" {
		t.Errorf("Dependencies[1].Image = %q", s.Dependencies[1].Image)
	}
	if s.Dependencies[1].Env["POSTGRES_PASSWORD"] != "kubox" {
		t.Errorf("Dependencies[1].Env POSTGRES_PASSWORD = %q", s.Dependencies[1].Env["POSTGRES_PASSWORD"])
	}
	if *s.App.Replicas != 1 {
		t.Errorf("App.Replicas = %d, want 1 (default)", *s.App.Replicas)
	}
	if s.Quotas.CPU != "2" {
		t.Errorf("Quotas.CPU = %q", s.Quotas.CPU)
	}
	if s.NetworkPolicy.Default != "deny-all" {
		t.Errorf("NetworkPolicy.Default = %q", s.NetworkPolicy.Default)
	}
	if s.Toolbox.Image != DefaultToolboxImage {
		t.Errorf("Toolbox.Image = %q", s.Toolbox.Image)
	}
}

func TestValidateRejectsMissingImage(t *testing.T) {
	s := &Slice{Name: "x"}
	if err := s.Validate(); err == nil {
		t.Error("expected error for missing app.image")
	}
	// even with build set: the image is the tag build produces and up deploys
	s = &Slice{Name: "x", App: App{Build: &Build{Context: "app"}}}
	if err := s.Validate(); err == nil {
		t.Error("expected error for missing app.image with build set")
	}
}

func TestValidateBuild(t *testing.T) {
	// context required when build is set
	noCtx := &Slice{Name: "x", App: App{Image: "img", Build: &Build{}}}
	if err := noCtx.Validate(); err == nil {
		t.Error("expected error for build without context")
	}

	// dockerfile defaults to Dockerfile in the context
	s := &Slice{Name: "x", App: App{Image: "img", Build: &Build{Context: "app"}}}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.App.Build.Dockerfile != "Dockerfile" {
		t.Errorf("Build.Dockerfile = %q, want Dockerfile (default)", s.App.Build.Dockerfile)
	}

	// explicit dockerfile is kept
	s = &Slice{Name: "x", App: App{Image: "img", Build: &Build{Context: "app", Dockerfile: "Dockerfile.prod"}}}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.App.Build.Dockerfile != "Dockerfile.prod" {
		t.Errorf("Build.Dockerfile = %q, want Dockerfile.prod", s.App.Build.Dockerfile)
	}
}

func TestValidateRejectsDependencyWithoutImage(t *testing.T) {
	s := &Slice{
		Name: "x",
		App:  App{Image: "img"},
		Dependencies: []Dependency{
			{Name: "db"},
		},
	}
	if err := s.Validate(); err == nil {
		t.Error("expected error for dependency without image")
	}
}

func TestValidateScalingDefaults(t *testing.T) {
	s := &Slice{Name: "x", App: App{Image: "img", Scaling: &Scaling{Max: 10}}}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.App.Scaling.Min != 1 {
		t.Errorf("Scaling.Min = %d, want 1 (default)", s.App.Scaling.Min)
	}
	if s.App.Scaling.CPUPercent != 70 {
		t.Errorf("Scaling.CPUPercent = %d, want 70 (default)", s.App.Scaling.CPUPercent)
	}
}

func TestValidateScalingErrors(t *testing.T) {
	tests := []struct {
		name    string
		scaling Scaling
	}{
		{"max missing", Scaling{Min: 1}},
		{"max below min", Scaling{Min: 3, Max: 2}},
		{"cpu percent too high", Scaling{Min: 1, Max: 2, CPUPercent: 150}},
	}
	for _, tt := range tests {
		s := &Slice{Name: "x", App: App{Image: "img", Scaling: &tt.scaling}}
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected error", tt.name)
		}
	}
}

func TestValidateHealthCheck(t *testing.T) {
	s := &Slice{Name: "x", App: App{Image: "img", HealthCheck: &HealthCheck{Path: "/healthz"}}}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.App.HealthCheck.InitialDelaySeconds != 5 {
		t.Errorf("InitialDelaySeconds = %d, want 5 (default)", s.App.HealthCheck.InitialDelaySeconds)
	}

	missing := &Slice{Name: "x", App: App{Image: "img", HealthCheck: &HealthCheck{}}}
	if err := missing.Validate(); err == nil {
		t.Error("expected error for missing healthcheck.path")
	}

	noSlash := &Slice{Name: "x", App: App{Image: "img", HealthCheck: &HealthCheck{Path: "healthz"}}}
	if err := noSlash.Validate(); err == nil {
		t.Error("expected error for healthcheck.path without leading slash")
	}
}

func TestEnvNamespace(t *testing.T) {
	if got := EnvNamespace("fix-auth"); got != "kubox-fix-auth" {
		t.Errorf("EnvNamespace = %q", got)
	}
}

func TestParseTTL(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", DefaultTTL, false},
		{"30m", 30 * time.Minute, false},
		{"1h", time.Hour, false},
		{"6h", 6 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"-1h", 0, true},
		{"bogus", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseTTL(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseTTL(%q): expected error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTTL(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseTTL(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
