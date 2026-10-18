package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kubotdev/kubox/internal/slice"
)

func contextWithTimeout(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cmd.Context(), timeoutFlag)
}

func newExecCmd() *cobra.Command {
	var create bool
	var deleteAfter bool
	var ttl string
	var sliceDir string
	cmd := &cobra.Command{
		Use:   "exec [env-name] -- <command> [args...]",
		Short: "Run a command inside an environment's toolbox pod",
		Long: "Runs a command inside the environment's toolbox pod — a helper " +
			"container with kubectl, curl, and common shell tools.\n\n" +
			"With --create, the environment is provisioned first (from --slice) " +
			"and deleted afterwards with --delete-after, even if the command " +
			"fails. The exit code is the command's own.",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: false,
		TraverseChildren:   true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmdArgs := cmd.Flags().Args()
			var envName string
			var runCmd []string

			sepIdx := indexOf(os.Args, "--")
			if sepIdx >= 0 {
				envName = firstNonFlag(cmdArgs)
				runCmd = os.Args[sepIdx+1:]
			} else {
				if len(cmdArgs) < 2 {
					return usageErrf("exec requires a command after `--`")
				}
				envName = cmdArgs[0]
				runCmd = cmdArgs[1:]
			}
			if envName == "" {
				return usageErrf("exec requires an environment name")
			}
			if len(runCmd) == 0 {
				return usageErrf("exec requires a command after `--`")
			}

			ttlDur, err := slice.ParseTTL(ttl)
			if err != nil {
				return usageErrf("%v", err)
			}

			m, err := newManager()
			if err != nil {
				return err
			}
			ctx, cancel := contextWithTimeout(cmd)
			defer cancel()

			if create {
				if sliceDir == "" {
					return usageErrf("--create requires --slice <dir>")
				}
				s, _, err := slice.Load(sliceDir)
				if err != nil {
					return err
				}
				s.Name = envName
				if _, err := m.Create(ctx, s, ttlDur); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Environment %q created.\n", envName)
			}

			if deleteAfter {
				defer func() {
					dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Minute)
					defer dcancel()
					_ = m.Delete(dctx, envName)
					fmt.Fprintf(cmd.OutOrStdout(), "Environment %q deleted.\n", envName)
				}()
			}

			res, err := m.ExecToolbox(ctx, envName, runCmd)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), res.Stdout)
			fmt.Fprint(os.Stderr, res.Stderr)
			if res.ExitCode != 0 {
				return exitError{res.ExitCode}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&create, "create", false, "create the environment first (requires --slice)")
	cmd.Flags().BoolVar(&deleteAfter, "delete-after", false, "delete the environment after the command finishes")
	cmd.Flags().StringVar(&ttl, "ttl", "1h", "TTL for the environment when using --create")
	cmd.Flags().StringVar(&sliceDir, "slice", "", "path to the slice directory (required with --create)")
	return cmd
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List live environments",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := newManager()
			if err != nil {
				return err
			}
			ctx, cancel := contextWithTimeout(cmd)
			defer cancel()

			envs, err := m.List(ctx)
			if err != nil {
				return err
			}
			if len(envs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No live environments.")
				return nil
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%-20s %-12s %-25s %s\n", "NAME", "STATUS", "EXPIRES", "SERVICES")
			for _, e := range envs {
				fmt.Fprintf(out, "%-20s %-12s %-25s %s\n",
					e.Name, e.Status, e.ExpiresAt.Format(time.RFC3339), strings.Join(e.Services, ","))
			}
			return nil
		},
	}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <env-name>",
		Short: "Show one environment's status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := newManager()
			if err != nil {
				return err
			}
			ctx, cancel := contextWithTimeout(cmd)
			defer cancel()

			e, err := m.Get(ctx, args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Environment:  %s\n", e.Name)
			fmt.Fprintf(out, "Namespace:   %s\n", e.Namespace)
			fmt.Fprintf(out, "Status:      %s\n", e.Status)
			fmt.Fprintf(out, "Created:     %s\n", e.CreatedAt.Format(time.RFC3339))
			fmt.Fprintf(out, "Expires:     %s\n", e.ExpiresAt.Format(time.RFC3339))
			if len(e.Services) > 0 {
				fmt.Fprintf(out, "Services:    %s\n", strings.Join(e.Services, ", "))
			}
			return nil
		},
	}
}

func indexOf(args []string, target string) int {
	for i, a := range args {
		if a == target {
			return i
		}
	}
	return -1
}

func firstNonFlag(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}
