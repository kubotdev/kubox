package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kubotdev/kubox/internal/env"
	"github.com/kubotdev/kubox/internal/k8s"
)

var version = "dev"

func appVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

const (
	exitFailure = 1
	exitNoAuth  = 2
	exitUsage   = 64
)

var (
	kubeconfigFlag string
	contextFlag    string
	timeoutFlag    time.Duration
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := &cobra.Command{
		Use:   "kubox",
		Short: "Disposable Kubernetes environments for AI agents",
		Long: "kubox gives AI coding agents (and humans) a disposable, isolated " +
			"Kubernetes environment to deploy, test, and verify application " +
			"changes — in seconds, not days.\n\n" +
			"Environments are namespace-scoped, quota-bound, network-isolated, " +
			"and carry a TTL so they can never leak. kubox is the provisioning " +
			"companion to kubot (diagnostics): kubot tells you what's wrong, " +
			"kubox gives you a box to fix it in.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       appVersion(),
	}
	root.AddCommand(newUpCmd())
	root.AddCommand(newDownCmd())
	root.AddCommand(newExecCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newTestCmd())
	root.AddCommand(newMCPCmd())

	root.PersistentFlags().StringVar(&kubeconfigFlag, "kubeconfig", "",
		"path to kubeconfig (default: KUBECONFIG or ~/.kube/config)")
	root.PersistentFlags().StringVar(&contextFlag, "context", "",
		"kubeconfig context to use")
	root.PersistentFlags().DurationVar(&timeoutFlag, "timeout", 5*time.Minute,
		"total wall-clock budget for the operation")

	if err := root.ExecuteContext(ctx); err != nil {
		var xe exitError
		if errors.As(err, &xe) {
			os.Exit(xe.code)
		}
		if isAuthErr(err) {
			fmt.Fprintln(os.Stderr, "kubox: "+err.Error())
			os.Exit(exitNoAuth)
		}
		fmt.Fprintln(os.Stderr, "kubox: "+err.Error())
		var ue usageError
		if errors.As(err, &ue) {
			os.Exit(exitUsage)
		}
		os.Exit(exitFailure)
	}
}

type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

type usageError struct{ error }

func usageErrf(format string, a ...any) error { return usageError{fmt.Errorf(format, a...)} }

type authError struct{ error }

func (e authError) Error() string { return e.error.Error() }

func isAuthErr(err error) bool {
	var ae authError
	return errors.As(err, &ae)
}

func newManager() (*env.Manager, error) {
	opts := k8s.Options{Kubeconfig: kubeconfigFlag, Context: contextFlag}
	cfg, err := k8s.BuildConfig(opts)
	if err != nil {
		return nil, authError{err}
	}
	cs, err := k8s.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return env.NewManager(cs, cfg, ""), nil
}
