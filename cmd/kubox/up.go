package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/kubotdev/kubox/internal/slice"
)

func newUpCmd() *cobra.Command {
	var name string
	var ttl string
	cmd := &cobra.Command{
		Use:   "up <slice-dir>",
		Short: "Deploy a slice into a disposable, isolated namespace",
		Long: "Reads kubox.yaml from the given directory and provisions an " +
			"environment: a namespace with resource quotas, a default-deny " +
			"network policy, a service account, the app under test, and its " +
			"in-cluster dependencies.\n\n" +
			"The environment carries a TTL; when it expires it is deleted " +
			"automatically. Always set a TTL so a crashed agent cannot leak " +
			"an environment.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := slice.Load(args[0])
			if err != nil {
				return err
			}
			if name != "" {
				s.Name = name
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

			envObj, err := m.Create(ctx, s, ttlDur)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Environment %q ready.\n", envObj.Name)
			fmt.Fprintf(out, "  namespace:  %s\n", envObj.Namespace)
			fmt.Fprintf(out, "  expires:    %s\n", envObj.ExpiresAt.Format(time.RFC3339))
			fmt.Fprintf(out, "  services:   %v\n", envObj.Services)
			fmt.Fprintf(out, "\nRun a command inside it:\n")
			fmt.Fprintf(out, "  kubox exec %s -- <command>\n", envObj.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "environment name (default: slice name)")
	cmd.Flags().StringVar(&ttl, "ttl", "1h", "time-to-live (e.g. 30m, 1h, 6h, 24h)")
	return cmd
}

func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down <env-name>",
		Short: "Delete an environment and everything in it",
		Long: "Deletes the environment's namespace and all resources inside it. " +
			"Idempotent: deleting a missing environment is not an error.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := newManager()
			if err != nil {
				return err
			}
			ctx, cancel := contextWithTimeout(cmd)
			defer cancel()

			if err := m.Delete(ctx, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Environment %q deleted.\n", args[0])
			return nil
		},
	}
}
