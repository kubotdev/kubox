package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kubotdev/kubox/internal/env"
	"github.com/kubotdev/kubox/internal/slice"
)

func newTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run tests against an environment",
		Long: "Built-in test types that provision (or reuse) an environment, " +
			"run the test, collect structured results, and clean up.\n\n" +
			"kubox does not make testing smart — the agent writes the k6 " +
			"script and interprets the results. kubox handles the environment " +
			"lifecycle and returns structured JSON.",
	}
	cmd.AddCommand(newTestLoadCmd())
	return cmd
}

func newTestLoadCmd() *cobra.Command {
	var envName string
	var script string
	var sliceDir string
	var create bool
	var deleteAfter bool
	var ttl string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "load [env-name] --script <file>",
		Short: "Run a k6 load test against an environment",
		Long: "Runs a k6 script inside the environment against the app's " +
			"Service endpoint. k6 evaluates the script's thresholds and exits " +
			"0 (pass) or 1 (fail); kubox returns the structured summary as JSON.\n\n" +
			"The script's BASE_URL is set automatically to the app's in-cluster " +
			"DNS name. Reference it in your script:\n\n" +
			"  export const options = {\n" +
			"    vus: 10,\n" +
			"    duration: '30s',\n" +
			"    thresholds: {\n" +
			"      http_req_duration: ['p(95)<200'],\n" +
			"      http_req_failed: ['rate<0.01'],\n" +
			"    },\n" +
			"  };\n" +
			"  export default function () {\n" +
			"    http.get(`${__ENV.BASE_URL}/healthz`);\n" +
			"  }",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				envName = args[0]
			}
			if envName == "" {
				return usageErrf("test load requires an environment name")
			}
			if script == "" {
				return usageErrf("test load requires --script <file>")
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

			// Optionally create the environment first.
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

			// Ensure cleanup.
			if deleteAfter {
				defer func() {
					dctx, dcancel := contextWithTimeout(cmd)
					defer dcancel()
					_ = m.Delete(dctx, envName)
					fmt.Fprintf(cmd.ErrOrStderr(), "Environment %q deleted.\n", envName)
				}()
			}

			resolved := env.ResolveScriptPath(sliceDir, script)

			result, err := m.RunLoadTest(ctx, envName, resolved)
			if err != nil {
				return err
			}

			if asJSON {
				b, _ := json.MarshalIndent(result, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				printK6Human(cmd, result)
			}
			if result.Status != "pass" {
				return exitError{exitFailure}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "environment name (or pass as positional arg)")
	cmd.Flags().StringVar(&script, "script", "", "path to the k6 script (.js)")
	cmd.Flags().StringVar(&sliceDir, "slice", "", "slice directory (for resolving relative script paths)")
	cmd.Flags().BoolVar(&create, "create", false, "create the environment first (requires --slice)")
	cmd.Flags().BoolVar(&deleteAfter, "delete-after", false, "delete the environment after the test")
	cmd.Flags().StringVar(&ttl, "ttl", "1h", "TTL for the environment when using --create")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output structured JSON instead of human-readable summary")
	return cmd
}

func printK6Human(cmd *cobra.Command, r *env.K6Result) {
	out := cmd.OutOrStdout()
	status := "PASS"
	if r.Status != "pass" {
		status = "FAIL"
	}
	fmt.Fprintf(out, "Load test: %s\n", status)
	fmt.Fprintf(out, "  requests:  %d\n", r.Summary.Requests)
	fmt.Fprintf(out, "  rps:       %.1f\n", r.Summary.RPS)
	fmt.Fprintf(out, "  p50:       %.1fms\n", r.Summary.P50)
	fmt.Fprintf(out, "  p95:       %.1fms\n", r.Summary.P95)
	fmt.Fprintf(out, "  p99:       %.1fms\n", r.Summary.P99)
	if len(r.Thresholds) > 0 {
		fmt.Fprintf(out, "  thresholds:\n")
		for name, t := range r.Thresholds {
			mark := "✓"
			if !t.Pass {
				mark = "✗"
			}
			fmt.Fprintf(out, "    %s %s = %.4g\n", mark, name, t.Value)
		}
	}
}
