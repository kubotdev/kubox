package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kubotdev/kubox/internal/k8s"
	"github.com/kubotdev/kubox/internal/slice"
)

var runCommand = func(ctx context.Context, w io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

func newBuildCmd() *cobra.Command {
	var noLoad bool
	cmd := &cobra.Command{
		Use:   "build <slice-dir>",
		Short: "Build the slice's app image with docker; load it into kind",
		Long: "Runs `docker build` for the slice's app.build.context and tags " +
			"the result as app.image — the exact tag `up` deploys.\n\n" +
			"If the current kubeconfig context is a kind cluster, the image " +
			"is also loaded into it with `kind load docker-image`, so pods " +
			"can pull it without a registry. Otherwise push the image to a " +
			"registry yourself before `up`.\n\n" +
			"Requires docker on PATH (and kind, for kind clusters). kubox " +
			"does not run a registry or a build service.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := buildContext(cmd)
			defer cancel()
			_, err := runBuild(ctx, args[0], noLoad, os.Stdout)
			return err
		},
	}
	cmd.Flags().BoolVar(&noLoad, "no-load", false,
		"build only; skip loading the image into a kind cluster")
	return cmd
}

func runBuild(ctx context.Context, sliceDir string, noLoad bool, w io.Writer) (string, error) {
	s, dir, err := slice.Load(sliceDir)
	if err != nil {
		return "", err
	}
	if s.App.Build == nil {
		return "", usageErrf("kubox.yaml: app.build.context is required for kubox build (or use an image from a registry directly)")
	}
	if s.App.Image == "" {
		return "", usageErrf("kubox.yaml: app.image is required — it is the tag this build produces")
	}

	contextDir := filepath.Join(dir, s.App.Build.Context)
	dockerfilePath := filepath.Join(contextDir, s.App.Build.Dockerfile)
	if _, err := os.Stat(dockerfilePath); err != nil {
		return "", fmt.Errorf("dockerfile %s: %w", dockerfilePath, err)
	}

	fmt.Fprintf(w, "building %s from %s\n", s.App.Image, contextDir)
	if err := runCommand(ctx, w, "docker", "build",
		"-t", s.App.Image,
		"-f", dockerfilePath,
		contextDir,
	); err != nil {
		return s.App.Image, fmt.Errorf("docker build: %w", err)
	}

	if noLoad {
		return s.App.Image, nil
	}
	cluster := kindClusterName(k8s.ResolveContextName(k8s.Options{
		Kubeconfig: kubeconfigFlag,
		Context:    contextFlag,
	}))
	if cluster == "" {
		fmt.Fprintln(w, "current context is not kind — push the image to a registry before `kubox up`")
		return s.App.Image, nil
	}
	fmt.Fprintf(w, "loading %s into kind cluster %s\n", s.App.Image, cluster)
	if err := runCommand(ctx, w, "kind", "load",
		"docker-image", s.App.Image,
		"--name", cluster,
	); err != nil {
		return s.App.Image, fmt.Errorf("kind load: %w", err)
	}
	return s.App.Image, nil
}

func buildContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	if cmd.Root().PersistentFlags().Changed("timeout") {
		return contextWithTimeout(cmd)
	}
	return context.WithTimeout(cmd.Context(), 15*time.Minute)
}

func kindClusterName(ctxName string) string {
	if !strings.HasPrefix(ctxName, "kind-") {
		return ""
	}
	return strings.TrimPrefix(ctxName, "kind-")
}
