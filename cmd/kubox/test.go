package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kubotdev/kubox/internal/env"
	"github.com/kubotdev/kubox/internal/slice"
)

func loadTestContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	if cmd.Root().PersistentFlags().Changed("timeout") {
		return contextWithTimeout(cmd)
	}
	return context.WithTimeout(cmd.Context(), 15*time.Minute)
}

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
	cmd.AddCommand(newTestDrillCmd())
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
	var report bool

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
			ctx, cancel := loadTestContext(cmd)
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

			// Preflight in report mode: an unhealthy environment is an
			// "error" (exit 3), not a failed check — the agent must not
			// "fix" app code when the sandbox itself is broken.
			var health *env.EnvHealth
			if report {
				h, err := m.CheckEnv(ctx, envName)
				if err != nil {
					return err
				}
				if !h.Healthy {
					printReport(cmd, env.NewReport(*h), asJSON)
					return exitError{exitEnvUnhealthy}
				}
				health = h
			}

			resolved := env.ResolveScriptPath(sliceDir, script)

			start := time.Now()
			result, err := m.RunLoadTest(ctx, envName, resolved)
			if err != nil {
				return err
			}

			if report {
				r := env.NewReport(*health, env.LoadCheck(result, time.Since(start)))
				printReport(cmd, r, asJSON)
				if r.Status != env.ReportPass {
					return exitError{exitFailure}
				}
				return nil
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
	cmd.Flags().BoolVar(&report, "report", false, "output the verification-contract envelope (environment health + findings) instead of the raw k6 result")
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
		keys := make([]string, 0, len(r.Thresholds))
		for name := range r.Thresholds {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			t := r.Thresholds[name]
			mark := "✓"
			if !t.Pass {
				mark = "✗"
			}
			fmt.Fprintf(out, "    %s %s = %.4g\n", mark, name, t.Value)
		}
	}
}

func newTestDrillCmd() *cobra.Command {
	var envName string
	var drillType string
	var dependency string
	var all bool
	var timeoutDur time.Duration
	var sliceDir string
	var ttl string
	var create bool
	var deleteAfter bool
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "drill [env-name]",
		Short: "Run a failure drill against an environment",
		Long: "Runs a standardized failure experiment and returns structured evidence.\n\n" +
			"Currently supported drill:\n\n" +
			"  dependency-restart  Delete a dependency's pods and verify the app\n" +
			"                      recovers: the dependency comes back Ready, the app\n" +
			"                      answers its health probe again, and replicas return\n" +
			"                      to full capacity. Passes with per-phase timings;\n" +
			"                      fails with a diagnosis of where recovery broke.\n\n" +
			"The environment is preflighted first: if it is unhealthy, no fault is\n" +
			"injected and the report says \"error\" (exit 3) — never \"fail\".\n\n" +
			"Output is always the verification-contract envelope (--json for\n" +
			"machines): status pass | fail | error, environment health, and\n" +
			"per-phase evidence.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				envName = args[0]
			}
			if envName == "" {
				return usageErrf("test drill requires an environment name")
			}
			if drillType == "" {
				drillType = env.DrillDependencyRestart
			}
			if all && dependency != "" {
				return usageErrf("--dependency and --all are mutually exclusive")
			}
			if !all && dependency == "" {
				return usageErrf("test drill requires --dependency <name> or --all")
			}

			ttlDur, err := slice.ParseTTL(ttl)
			if err != nil {
				return usageErrf("%v", err)
			}

			m, err := newManager()
			if err != nil {
				return err
			}
			ctx, cancel := loadTestContext(cmd)
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

			targets := []string{dependency}
			if all {
				targets, err = drillTargets(ctx, m, envName)
				if err != nil {
					return err
				}
				if len(targets) == 0 {
					return fmt.Errorf("environment %q has no dependencies to drill", envName)
				}
			}

			var checks []env.Check
			var health env.EnvHealth
			for _, target := range targets {
				res, err := m.RunDrill(ctx, envName, env.DrillSpec{
					Type:       drillType,
					Dependency: target,
					Timeout:    timeoutDur,
				})
				if err != nil {
					return err
				}
				health = res.Environment
				if !health.Healthy {
					break // checks not run; the report says "error"
				}
				checks = append(checks, res.Check)
			}

			rep := env.NewReport(health, checks...)
			printReport(cmd, rep, asJSON)
			switch rep.Status {
			case env.ReportError:
				return exitError{exitEnvUnhealthy}
			case env.ReportFail:
				return exitError{exitFailure}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "environment name (or pass as positional arg)")
	cmd.Flags().StringVar(&drillType, "type", env.DrillDependencyRestart, "drill type (currently: dependency-restart)")
	cmd.Flags().StringVar(&dependency, "dependency", "", "dependency to target (a service of the environment)")
	cmd.Flags().BoolVar(&all, "all", false, "run the drill against every dependency in the environment")
	cmd.Flags().DurationVar(&timeoutDur, "timeout", 2*time.Minute, "per-phase observation budget")
	cmd.Flags().StringVar(&sliceDir, "slice", "", "slice directory (for --create)")
	cmd.Flags().StringVar(&ttl, "ttl", "1h", "TTL for the environment when using --create")
	cmd.Flags().BoolVar(&create, "create", false, "create the environment first (requires --slice)")
	cmd.Flags().BoolVar(&deleteAfter, "delete-after", false, "delete the environment after the drills")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output the verification-contract envelope as JSON")
	return cmd
}

// drillTargets lists the environment's dependencies: every service except
// the app service (whose name equals the environment name).
func drillTargets(ctx context.Context, m *env.Manager, envName string) ([]string, error) {
	e, err := m.Get(ctx, envName)
	if err != nil {
		return nil, err
	}
	var deps []string
	for _, svc := range e.Services {
		if svc != envName {
			deps = append(deps, svc)
		}
	}
	return deps, nil
}

func printReport(cmd *cobra.Command, r *env.Report, asJSON bool) {
	if asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(b))
		return
	}
	out := cmd.OutOrStdout()
	switch r.Status {
	case env.ReportPass:
		fmt.Fprintln(out, "Verification: PASS")
	case env.ReportFail:
		fmt.Fprintln(out, "Verification: FAIL (environment healthy)")
	default:
		fmt.Fprintln(out, "Verification: ERROR — environment unhealthy, checks not run")
	}
	fmt.Fprintf(out, "  environment: %s (%s)\n", r.Environment.Namespace,
		map[bool]string{true: "healthy", false: "UNHEALTHY"}[r.Environment.Healthy])
	for _, p := range r.Environment.Problems {
		comp := p.Component
		if comp == "" {
			comp = "-"
		}
		fmt.Fprintf(out, "    ✗ [%s] %s: %s\n", p.Kind, comp, p.Message)
	}
	for _, c := range r.Checks {
		fmt.Fprintf(out, "  %s: %s", c.Type, strings.ToUpper(string(c.Status)))
		if c.DurationMS > 0 {
			fmt.Fprintf(out, " (%s)", (time.Duration(c.DurationMS) * time.Millisecond).Round(100*time.Millisecond))
		}
		fmt.Fprintln(out)
		for _, f := range c.Findings {
			fmt.Fprintf(out, "    %s %s\n", checkMark(c.Status), f.Message)
		}
		printDrillPhases(out, c)
	}
}

func printDrillPhases(out io.Writer, c env.Check) {
	if c.Type != env.CheckTypeDrillDepRestart || len(c.Detail) == 0 {
		return
	}
	var d env.DrillDetail
	if err := json.Unmarshal(c.Detail, &d); err != nil {
		return
	}
	for _, ph := range d.Phases {
		mark := "✓"
		if ph.Status == "skipped" {
			mark = "-"
		} else if ph.Status != "pass" {
			mark = "✗"
		}
		dur := ""
		if ph.DurationMS > 0 {
			dur = (time.Duration(ph.DurationMS) * time.Millisecond).Round(100 * time.Millisecond).String()
		}
		fmt.Fprintf(out, "    %s %-20s %-8s %s\n", mark, ph.Name, dur, ph.Message)
	}
}

func checkMark(s env.CheckStatus) string {
	if s == env.CheckPass {
		return "✓"
	}
	return "✗"
}
