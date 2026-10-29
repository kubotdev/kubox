# Architecture

```
your cluster
  └── namespace kubox-<name>          (created and deleted by kubox)
        ├── app + dependencies        (from the slice, labels kubox.dev/env=<name>)
        ├── ResourceQuota             (namespace-wide ceiling)
        ├── NetworkPolicy             (default deny, intra-slice + DNS only)
        ├── ServiceAccount            (namespace-scoped edit, no cluster-admin)
        └── kubox.dev/expires-at      (TTL annotation, swept by every command)
              ↑
internal/env — Manager: create · re-sync · exec · sweep · load · drill
              ↑
cmd/kubox (CLI)      internal/mcp (stdio server)      skill (judgment layer)
              ↑
           agent / CI / human
```

Two product layers, one Manager underneath (DESIGN.md's split): the
**sandbox layer** (up/down/exec/list/status — lifecycle + guardrails) and
the **test layer** (test load/test drill — measurement on top). The test
layer has no way to run without the sandbox layer's guarantees: quotas,
network policy, TTL, preflight.

## Packages

| Area | Package | Notes |
|---|---|---|
| Commands | `cmd/kubox` | one file per command + `main` (exit codes, manager bootstrap, TTL sweep); no logic beyond flags |
| Slice | `internal/slice` | `kubox.yaml` parse, validation, defaults; `EnvNamespace` (`kubox-<name>`) |
| Manager | `internal/env` | namespace/deploy/quota/netpol/RBAC lifecycle, re-sync + prune, toolbox exec, one-shot runner, k6 pod runner, drill engine, verification envelope, `CheckEnv` preflight |
| Client | `internal/k8s` | kubeconfig resolution (kubectl semantics), clientset |
| MCP | `internal/mcp` | generic stdio JSON-RPC server (tools/prompts/resources); no kubox specifics — tools are wired in `cmd/kubox/mcp.go` |

Conventions: mutation only ever targets namespaces labeled
`kubox.dev/env`; kubox owns lifecycle and evidence — `kubox build` is a
`docker build` + `kind load` passthrough, never a registry or build
service; tests use fake clients (never a live cluster), with injectable
seams (`waitFn`, `probeFn`, `runCommand`) where a real cluster would
otherwise be needed.

## Lifecycle

- **Create** blocks until every deployment is Ready; a live environment of
  the same name is **re-synced** instead: resources applied, pods rolled,
  pruned of removed services, TTL refreshed.
- **Delete** waits for the namespace to be gone.
- **TTL** is sweep-on-invocation: every command deletes expired
  environments first (`kubox.dev/expires-at`). No controller pod in v0.x —
  the backstop holds as long as *something* runs kubox.

## The verification path

`test load --report` and `test drill` share one pipeline: `CheckEnv`
preflight (namespace, containers, deployments, scheduling, app endpoints)
→ run the check (k6 thresholds / drill phases) → one [envelope](json.md).
The preflight is what makes `error` (environment broken, checks not run)
distinguishable from `fail` (the app lost) — the property agents build on.
