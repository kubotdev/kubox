---
name: kubernetes-verification
description: Give this agent an isolated, disposable Kubernetes environment to break, fix, and verify a service with kubox. Use when testing a Kubernetes-deployable app, load testing, scaling/recovery/OOM drills, re-syncing after a fix, or when the user mentions kubox, a sandbox, an ephemeral environment, or "prove it works in k8s".
---

# Kubernetes verification with kubox

kubox deploys your app plus its immediate neighbors into a throwaway
namespace — quota-bound, default-deny network policy, TTL'd — runs k6 against
it, and tears it down. The environment is where breaking things is safe; the
k6 script's **thresholds are the pass/fail signal** you act on. Your job is to
run the loop, collect the evidence, and report before/after — never to invent
results or tune thresholds until they pass.

## When to use this skill

- "prove it works in k8s", "test my service", "load test this"
- "does it recover when the pod/db dies", scaling or OOM drills
- Any break → fix → verify loop around a Kubernetes-deployable app
- The user mentions kubox, a sandbox, an ephemeral environment

## The loop

1. Define the slice (`kubox.yaml` below) — app + its real dependencies.
2. `kubox up` — deploy. Re-running `up` after a rebuild **re-syncs** (rolls pods, keeps data volumes).
3. Break it or load it. Watch it fail. Keep the evidence (p95, error rate, events, logs).
4. Diagnose → fix → rebuild (`kubox build`) → `kubox up` again (same name).
5. Prove it: the **same test** must now pass. Report before/after numbers.
6. `kubox down` — always, even after failures.

## How to run it

**If the kubox MCP server is connected**, use its tools: `up`, `build`,
`exec`, `test_load`, `list`, `status`, `down`. Always pass a `ttl` to `up`.

**Otherwise the CLI.** Namespace is always `kubox-<name>`.

| Question | Command |
|---|---|
| Build the slice's app image | `kubox build <dir>` (`docker build` + `kind load`; `app.build.context` in the slice) |
| Deploy / update a slice | `kubox up <dir> --name <env> --ttl 1h` |
| What's alive / when does it expire? | `kubox list` · `kubox status <env>` |
| Run something inside the environment | `kubox exec <env> -- <cmd>` (kubectl, curl, sh) |
| Is it fast/robust enough? | `kubox test load <env> --script load.js` (`--json` for machines) |
| Tear it down | `kubox down <env>` (waits until gone) |
| One-shot create→run→delete | `kubox exec --create <env> --slice <dir> --ttl 1h --delete-after -- <cmd>` |

## The slice (`kubox.yaml`)

```yaml
name: shop
app:
  image: shop:v1              # produced by `kubox build` (app.build below) or a registry image
  port: 8080
  env: {DATABASE_URL: postgres://postgres:kubox@postgres:5432/shop}
  resources: {limitsCPU: "1", limitsMemory: 512Mi}   # requests default 50m/64Mi
  healthcheck: {path: /healthz}                      # readiness + liveness probes
  scaling: {min: 2, max: 8, cpuPercent: 70}          # managed HPA (needs metrics-server)
dependencies:
  - name: postgres
    image: postgres:16-alpine
    port: 5432
    env: {POSTGRES_PASSWORD: kubox}
    volume: {size: 1Gi, path: /var/lib/postgresql/data}  # survives re-sync
quotas: {cpu: "2", memory: 4Gi, pods: "20"}          # namespace-wide caps
```

## Playbooks

### Load (the core signal)
Write k6 with explicit thresholds (`http_req_duration: ['p(95)<100']`). `test load`
exits non-zero when they're violated — that's the signal to **fix, not loosen**.
Save the failing run's JSON for the after-comparison.

### Recovery
- Dependency first: `kubox test drill <env> --dependency postgres --json` — the
  standardized drill: deletes the dep's pods, times recovery on both sides, and
  returns a diagnosis (dep slow to restart vs app missing reconnect logic).
- App: `kubox exec <env> -- kubectl delete pod -l app=<svc>` — watch recreation + probe.
- Then probe health + a quick load loop — reconnect bugs (SYN_SENT, pool exhaustion) surface here.

### Horizontal scaling
- Declare `app.scaling` in the slice, or manual:
  `kubox exec <env> -- kubectl autoscale deployment <svc> --min=2 --max=10 --cpu-percent=50`.
- Crush with load; watch `kubectl get hpa -w` and `kubectl get pods -w`.

### Resource pressure
- Tight `limitsCPU`/`limitsMemory` → re-sync → crush. Watch throttle/OOMKill (exit 137) + restart.

### Images
`kubox build <slice>` (`app.build.context` in the slice) → `kubox up` again, or the
manual path: `docker build -t <img> .` → `kind load docker-image <img> --name <cluster>`
(or push to a registry). Pods roll to the new image; volumes survive.

## First run — when something is missing

Don't stall; each gap has one fix:

- **`kubox` not on PATH:** install — `curl -fsSL https://raw.githubusercontent.com/kubotdev/kubox/main/skills/install.sh | sh` (or `go install github.com/kubotdev/kubox/cmd/kubox@latest`).
- **No cluster / kubeconfig:** `kind create cluster` (~30s), or ask the user for the context. kubox resolves kubeconfig exactly like kubectl.
- **ImagePullBackOff after `up`:** the image isn't in the cluster — `kubox build <slice>` (or build + `kind load docker-image` / registry push yourself); `up` again to re-sync.
- **`failed quota` on pod creation:** raise `quotas` — it must cover app + deps + toolbox (50m/64Mi) + k6 runner (100m/64Mi).
- **HPA not moving under load:** metrics-server missing — `kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml`.
- **MCP load tests die at ~10s:** set the kubox MCP server `timeout` to `180000`.

## Rules — non-negotiable

1. **Always set a TTL, always `down` when done** — even after a failed run or a crash.
   A leaked environment is a bug you caused.
2. **Thresholds are the contract.** A FAIL means fix the code. Never edit thresholds
   to turn a failing run green, and never report a run you didn't actually complete.
3. **Keep the failing numbers.** Report before/after with kubot's (or kubectl's)
   evidence: p95/p99, error rate, restarts, exit codes. One table beats adjectives.
4. **Re-sync, don't recreate.** Same name = in-place update with data volumes intact.
   Delete only when you intentionally want a from-scratch state.
5. **kubox build is a convenience, not a build service.** It runs `docker build` +
   `kind load` for kind. Remote clusters: registry push is yours. Then re-sync.
6. **Diagnose before fixing.** If kubot is installed: `kubot inspect -n kubox-<env>`
   (read-only, findings-first). Otherwise: `kubectl describe/logs/events` via exec.
   Never guess a root cause the data doesn't support.
7. **`exec` is one-shot, non-interactive.** Use `sh -c '...'` for pipes and loops.
8. **The toolbox SA is namespace-scoped `edit`.** Scale/delete/patch work inside the
   environment; nodes, namespaces, and PVs are out of reach — by design.
9. **Read the failure kind before fixing.** In verification output (`test load --report`,
   `test drill`), `status: "error"` + exit 3 means the *environment* is unhealthy and
   checks didn't run — fix the env/slice (image, quota, crashlooping dep) and re-run;
   never change app code for an `error`. Only `status: "fail"` + exit 1 implicates
   your change.

## Output shape

1. One line: verdict + environment name (e.g. "FAIL → fixed → PASS · env shop").
2. Before/after table: p95, p99, error rate, rps (from the two `test load` runs).
3. Root cause in one sentence, with the evidence that proves it (event, exit code, log line).
4. Cleanup confirmation: `down` ran (or TTL will reap it, with the expiry time).

## Reading the signals

- **p95 tail + client-side timeouts, TIME_WAIT pileup:** connection/pool saturation —
  look at the HTTP client's keep-alives and pool size.
- **SYN_SENT after a dependency restart:** the app never reconnects — missing retry/reconnect logic.
- **OOMKilled (exit 137) + restarts climbing:** memory limit too low or a leak; `kubectl describe` carries the evidence.
- **HPA pinned at max under load:** raise max, or you've hit the namespace quota ceiling.
- **Pods Pending:** `failed quota` — quotas must cover every pod in the slice.

## Safety and privacy

Environments are real namespaces on a real cluster: isolated (default-deny
NetworkPolicy + ResourceQuota + scoped SA) but still yours. Keep names obvious,
TTLs short, and production contexts out of reach. Volumes and everything else
die with the environment — treat each one as disposable.
