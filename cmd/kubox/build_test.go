package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type call struct {
	name string
	args []string
}

func stubRun(t *testing.T, err error) *[]call {
	t.Helper()
	var calls []call
	orig := runCommand
	runCommand = func(_ context.Context, _ io.Writer, name string, args ...string) error {
		calls = append(calls, call{name: name, args: args})
		return err
	}
	t.Cleanup(func() { runCommand = orig })
	return &calls
}

func writeSliceWithBuild(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubox.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(dir, "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const buildSliceYAML = `name: shop
app:
  image: shop:v1
  port: 8080
  build:
    context: app
`

func execBuild(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newBuildCmd()
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestBuildRunsDockerAndKindLoad(t *testing.T) {
	dir := writeSliceWithBuild(t, buildSliceYAML)
	calls := stubRun(t, nil)
	contextFlag = "kind-kubot-demo"
	t.Cleanup(func() { contextFlag = "" })

	if err := execBuild(t, dir); err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("got %d calls, want 2 (docker build + kind load): %+v", len(*calls), *calls)
	}

	docker := (*calls)[0]
	if docker.name != "docker" {
		t.Errorf("first call = %q, want docker", docker.name)
	}
	want := []string{"build", "-t", "shop:v1", "-f", filepath.Join(dir, "app", "Dockerfile"), filepath.Join(dir, "app")}
	if strings.Join(docker.args, "|") != strings.Join(want, "|") {
		t.Errorf("docker args = %v, want %v", docker.args, want)
	}

	kind := (*calls)[1]
	if kind.name != "kind" {
		t.Errorf("second call = %q, want kind", kind.name)
	}
	kindWant := []string{"load", "docker-image", "shop:v1", "--name", "kubot-demo"}
	if strings.Join(kind.args, "|") != strings.Join(kindWant, "|") {
		t.Errorf("kind args = %v, want %v", kind.args, kindWant)
	}
}

func TestBuildNoLoadSkipsKind(t *testing.T) {
	dir := writeSliceWithBuild(t, buildSliceYAML)
	calls := stubRun(t, nil)
	contextFlag = "kind-kubot-demo"
	t.Cleanup(func() { contextFlag = "" })

	if err := execBuild(t, dir, "--no-load"); err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0].name != "docker" {
		t.Fatalf("got %+v, want only docker build", *calls)
	}
}

func TestBuildContextNotKindSkipsLoad(t *testing.T) {
	dir := writeSliceWithBuild(t, buildSliceYAML)
	calls := stubRun(t, nil)
	contextFlag = "prod-cluster"
	t.Cleanup(func() { contextFlag = "" })

	if err := execBuild(t, dir); err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0].name != "docker" {
		t.Fatalf("got %+v, want only docker build", *calls)
	}
}

func TestBuildRequiresBuildContext(t *testing.T) {
	dir := t.TempDir()
	yaml := "name: shop\napp:\n  image: shop:v1\n  port: 8080\n"
	if err := os.WriteFile(filepath.Join(dir, "kubox.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := stubRun(t, nil)
	if err := execBuild(t, dir); err == nil {
		t.Error("expected error for slice without app.build")
	}
	if len(*calls) != 0 {
		t.Errorf("no commands should run, got %+v", *calls)
	}
}

func TestBuildDockerFailurePropagates(t *testing.T) {
	dir := writeSliceWithBuild(t, buildSliceYAML)
	stubRun(t, errors.New("exit status 1"))
	contextFlag = "kind-demo"
	t.Cleanup(func() { contextFlag = "" })

	err := execBuild(t, dir)
	if err == nil || !strings.Contains(err.Error(), "docker build") {
		t.Errorf("err = %v, want wrapped docker build failure", err)
	}
}

func TestKindClusterName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"kind-kubot-demo", "kubot-demo"},
		{"kind-", ""},
		{"prod", ""},
		{"", ""},
		{"docker-desktop", ""},
	}
	for _, tt := range tests {
		if got := kindClusterName(tt.in); got != tt.want {
			t.Errorf("kindClusterName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
