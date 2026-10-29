package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/kubotdev/kubox/internal/env"
	"github.com/kubotdev/kubox/internal/mcp"
	"github.com/kubotdev/kubox/internal/slice"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run as an MCP server over stdio (for AI agents)",
		Long: "Speaks the Model Context Protocol on stdin/stdout. Configure it " +
			"in an MCP client (Claude Desktop/Code, Cursor, …) and the agent " +
			"gains tools to create, use, test, and delete disposable " +
			"Kubernetes environments.\n\n" +
			"Point KUBECONFIG at the target cluster in the server's env so " +
			"tools need no connection argument.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv := &mcp.Server{
				Name:    "kubox",
				Version: appVersion(),
				Instructions: "kubox provisions disposable, isolated Kubernetes " +
					"environments. Use `up` to create one from a slice, `exec` " +
					"to run commands inside it, `build` to build the slice's " +
					"app image (docker + `kind load` when the context is kind), " +
					"`test_load` to run a k6 load " +
					"test, `test_drill` to run failure drills (restart a " +
					"dependency and verify the app recovers), and `down` to " +
					"delete it. Always set a TTL. Environments " +
					"are namespace-scoped and network-isolated; they cannot touch " +
					"production. `build` handles the docker + `kind load` step " +
					"for kind clusters; for anything else push the image to a " +
					"registry before `up`/re-sync so pods pull the new version. " +
					"If something is unhealthy, diagnose before fixing: if kubot " +
					"is installed on the host, run `kubot inspect -n <namespace>` " +
					"(read-only, same kubeconfig) and follow its findings; " +
					"otherwise use `kubox exec` with kubectl describe/logs/events.",
				Tools: kuboxTools(),
			}
			fmt.Fprintln(os.Stderr, "kubox mcp: serving on stdio (ctrl-c to stop)")
			return srv.Serve(cmd.Context(), os.Stdin, os.Stdout)
		},
	}
}

func kuboxTools() []mcp.Tool {
	return []mcp.Tool{
		{
			Name: "up",
			Description: "Create a disposable Kubernetes environment from a slice " +
				"directory; if it already exists, re-sync it (update resources, " +
				"refresh ttl, roll pods to pick up rebuilt images). Returns the " +
				"environment name, namespace, and services. Always set a ttl so " +
				"a crashed agent cannot leak the environment.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"slice_dir", "name"},
				"properties": map[string]any{
					"slice_dir": map[string]any{
						"type":        "string",
						"description": "Path to the directory containing kubox.yaml",
					},
					"name": map[string]any{
						"type":        "string",
						"description": "Environment name (lowercase, hyphens). Becomes the namespace suffix.",
					},
					"ttl": map[string]any{
						"type":        "string",
						"description": "Time-to-live, e.g. '30m', '1h', '6h'. Default 1h.",
					},
				},
			},
			Handler: upTool,
		},
		{
			Name: "build",
			Description: "Build the slice's app image with docker and load it " +
				"into the kind cluster when the kubeconfig context is kind. " +
				"Requires app.build.context in the slice and docker (plus " +
				"kind) on the host. Returns the image tag built.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"slice_dir"},
				"properties": map[string]any{
					"slice_dir": map[string]any{
						"type":        "string",
						"description": "Path to the directory containing kubox.yaml",
					},
					"no_load": map[string]any{
						"type":        "boolean",
						"description": "Build only; skip loading the image into a kind cluster.",
					},
				},
			},
			Handler: buildTool,
		},
		{
			Name: "down",
			Description: "Delete a disposable Kubernetes environment and everything " +
				"in it. Idempotent — deleting a missing environment is not an error.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Environment name.",
					},
				},
			},
			Handler: downTool,
		},
		{
			Name: "exec",
			Description: "Run a command inside an environment's toolbox pod " +
				"(kubectl, curl, shell). Returns exit code, stdout, stderr.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"name", "command"},
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Environment name.",
					},
					"command": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Command and arguments, e.g. [\"kubectl\", \"get\", \"pods\"]",
					},
				},
			},
			Handler: execTool,
		},
		{
			Name: "list",
			Description: "List every live kubox environment with its status and " +
				"expiry.",
			InputSchema: map[string]any{"type": "object"},
			Handler:     listTool,
		},
		{
			Name:        "status",
			Description: "Get one environment's status, namespace, and expiry.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Environment name.",
					},
				},
			},
			Handler: statusTool,
		},
		{
			Name: "test_load",
			Description: "Run a k6 load test against an environment's app endpoint. " +
				"Returns pass/fail (from the script's thresholds) plus a metrics " +
				"summary (p50/p95/p99, RPS, error rate) as JSON.",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"name", "script"},
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Environment name.",
					},
					"script": map[string]any{
						"type":        "string",
						"description": "Path to the k6 script (.js) on the local filesystem.",
					},
				},
			},
			Handler: testLoadTool,
		},
		{
			Name: "test_drill",
			Description: "Run a failure drill against an environment (restart a " +
				"dependency and verify the app recovers). Preflights the " +
				"environment first: if it is unhealthy no fault is injected " +
				"and the report status is \"error\", not \"fail\". Returns a " +
				"verification report: status pass|fail|error, environment " +
				"health, and per-phase evidence (recovery timings).",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"name"},
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Environment name.",
					},
					"type": map[string]any{
						"type":        "string",
						"description": "Drill type. Currently only 'dependency-restart' (default).",
					},
					"dependency": map[string]any{
						"type":        "string",
						"description": "Dependency to target (a service of the environment). Mutually exclusive with 'all'.",
					},
					"all": map[string]any{
						"type":        "boolean",
						"description": "Run the drill against every dependency in the environment.",
					},
					"timeout": map[string]any{
						"type":        "string",
						"description": "Per-phase observation budget, e.g. '2m'. Default 2m.",
					},
				},
			},
			Handler: testDrillTool,
		},
	}
}

func upTool(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		SliceDir string `json:"slice_dir"`
		Name     string `json:"name"`
		TTL      string `json:"ttl"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	s, _, err := slice.Load(args.SliceDir)
	if err != nil {
		return "", err
	}
	if args.Name != "" {
		s.Name = args.Name
	}
	ttlDur, err := slice.ParseTTL(args.TTL)
	if err != nil {
		return "", err
	}
	m, err := newManager()
	if err != nil {
		return "", err
	}
	e, err := m.Create(ctx, s, ttlDur)
	if err != nil {
		return "", err
	}
	return marshalJSON(e)
}

func buildTool(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		SliceDir string `json:"slice_dir"`
		NoLoad   bool   `json:"no_load"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	var log bytes.Buffer
	image, err := runBuild(ctx, args.SliceDir, args.NoLoad, &log)
	if err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"status": "built",
		"image":  image,
		"log":    log.String(),
	})
}

func downTool(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	m, err := newManager()
	if err != nil {
		return "", err
	}
	if err := m.Delete(ctx, args.Name); err != nil {
		return "", err
	}
	return marshalJSON(map[string]string{"status": "deleted", "name": args.Name})
}

func execTool(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Name    string   `json:"name"`
		Command []string `json:"command"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if len(args.Command) == 0 {
		return "", fmt.Errorf("command is required")
	}
	m, err := newManager()
	if err != nil {
		return "", err
	}
	res, err := m.ExecToolbox(ctx, args.Name, args.Command)
	if err != nil {
		return "", err
	}
	return marshalJSON(res)
}

func listTool(ctx context.Context, _ json.RawMessage) (string, error) {
	m, err := newManager()
	if err != nil {
		return "", err
	}
	envs, err := m.List(ctx)
	if err != nil {
		return "", err
	}
	return marshalJSON(envs)
}

func statusTool(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	m, err := newManager()
	if err != nil {
		return "", err
	}
	e, err := m.Get(ctx, args.Name)
	if err != nil {
		return "", err
	}
	return marshalJSON(e)
}

func testLoadTool(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Name   string `json:"name"`
		Script string `json:"script"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	m, err := newManager()
	if err != nil {
		return "", err
	}
	resolved := env.ResolveScriptPath("", args.Script)
	result, err := m.RunLoadTest(ctx, args.Name, resolved)
	if err != nil {
		return "", err
	}
	return marshalJSON(result)
}

func testDrillTool(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		Dependency string `json:"dependency"`
		All        bool   `json:"all"`
		Timeout    string `json:"timeout"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	drillType := args.Type
	if drillType == "" {
		drillType = env.DrillDependencyRestart
	}
	if args.All && args.Dependency != "" {
		return "", fmt.Errorf("'dependency' and 'all' are mutually exclusive")
	}
	if !args.All && args.Dependency == "" {
		return "", fmt.Errorf("either 'dependency' or 'all' is required")
	}
	var timeoutDur time.Duration
	if args.Timeout != "" {
		d, err := time.ParseDuration(args.Timeout)
		if err != nil {
			return "", fmt.Errorf("invalid timeout %q: %w", args.Timeout, err)
		}
		timeoutDur = d
	}

	m, err := newManager()
	if err != nil {
		return "", err
	}
	targets := []string{args.Dependency}
	if args.All {
		e, err := m.Get(ctx, args.Name)
		if err != nil {
			return "", err
		}
		targets = targets[:0]
		for _, svc := range e.Services {
			if svc != args.Name {
				targets = append(targets, svc)
			}
		}
		if len(targets) == 0 {
			return "", fmt.Errorf("environment %q has no dependencies to drill", args.Name)
		}
	}

	var checks []env.Check
	var health env.EnvHealth
	for _, target := range targets {
		res, err := m.RunDrill(ctx, args.Name, env.DrillSpec{
			Type:       drillType,
			Dependency: target,
			Timeout:    timeoutDur,
		})
		if err != nil {
			return "", err
		}
		health = res.Environment
		if !health.Healthy {
			break
		}
		checks = append(checks, res.Check)
	}
	return marshalJSON(env.NewReport(health, checks...))
}

func marshalJSON(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
