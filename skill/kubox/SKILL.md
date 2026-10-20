---
name: kubox
description: Use kubox to give this agent an isolated, disposable Kubernetes environment to break, fix, and verify a service. Use when testing a Kubernetes-deployable app, load testing, scaling/recovery/OOM drills, or when the user mentions kubox, a sandbox, an ephemeral environment, or "prove it works in k8s".
---

# kubox — break, fix, verify

kubox creates a throwaway namespace (app + dependencies + quotas + network isolation)
from a small `kubox.yaml`, runs load tests against it, and tears it down. TTL ensures
nothing leaks. Your loop:

1. Define the slice (app + its real dependencies, like postgres/redis).
2. `kubox up` — deploy. Re-running `up` after a rebuild re-syncs (rolls pods, keeps data volumes).
3. Break or load it. Watch it fail. Get evidence (p95, error rate, events, logs).
4. Diagnose → fix → rebuild → `kubox up` again (same name re-syncs).
5. Prove the fix: same test must now pass. Report before/after numbers.
6. `kubox down` — always clean up, even after failures.

## Commands

- `kubox up <dir> --name <env> --ttl 1h` — create/re-sync. Namespace is `kubox-<env>`.
- `kubox exec <env> -- <cmd...>` — one-shot command in the toolbox pod (kubectl, curl, sh).
- `kubox test_load <env> <script.js>` — k6 against the app endpoint; exits non-zero on threshold failure.
- `kubox list` / `kubox status <env>` — what's alive, what expires when.
- `kubox down <env>` — deletes the namespace and waits until it's gone.

## kubox.yaml (the slice)

```yaml
name: shop
app:
  image: shop:v1            # or app.build.context for future builds
  port: 8080
  env:
    DATABASE_URL: postgres://postgres:kubox@postgres:5432/shop
  resources:                 # optional: requests default 50m/64Mi
    limitsCPU: "1"
    limitsMemory: 512Mi
  healthcheck:               # optional but strongly recommended
    path: /healthz
  scaling:                   # optional: adds an HPA on the app
    min: 2
    max: 8
    cpuPercent: 70
dependencies:
  - name: postgres
    image: postgres:16-alpine
    port: 5432
    env: {POSTGRES_PASSWORD: kubox}
    volume: {size: 1Gi, path: /var/lib/postgresql/data}  # data survives re-sync
quotas: {cpu: "2", memory: 4Gi, pods: "20"}              # namespace-wide caps
```

## Playbooks

### Load test (the core loop)
Write a k6 script with explicit thresholds (e.g. `http_req_duration: ['p(95)<100']`).
`kubox test_load` FAILS when thresholds are violated — that's the signal to fix, not
to loosen. Keep a copy of the failing run's JSON (p95/p99/rps/errors) for before/after.

### Recovery
- App pod: `kubox exec <env> -- kubectl delete pod -l app=<svc>` — Deployment recreates it.
- Dependency: `kubox exec <env> -- kubectl delete pod -l app=postgres` (with a volume, data survives).
- Verify health returns to normal (probe path + a quick load or curl loop).
- Check the app's reconnect behavior — this is where SYN_SENT/连接 pool bugs show up.

### Horizontal scaling
- Requires metrics-server on the cluster (kind: `kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml`).
- Declare `app.scaling` in the slice (HPA managed by kubox), or go manual:
  `kubox exec <env> -- kubectl autoscale deployment <svc> --min=2 --max=10 --cpu-percent=50`.
- Then crush with load, watch: `kubox exec <env> -- kubectl get hpa -w`.

### Vertical / resource pressure
- Set tight `resources.limitsCPU` / `limitsMemory` in the slice, re-sync, crush with load.
- Watch throttle/OOMKill + restart: `kubox exec <env> -- kubectl get pods -w` and describe.

### Images
kubox does NOT build. Build with docker, then `kind load docker-image <img> --name <cluster>`
(or push to a registry). Re-run `kubox up` with the same name — pods roll to the new image,
data volumes survive.

## Diagnosis

- If [kubot](https://github.com/kubotdev/kubot) is installed on the host:
  `kubot inspect -n kubox-<env>` — read-only, findings-first; follow its findings.
- Otherwise (always available): `kubox exec <env> -- kubectl describe pod <pod>`,
  `kubectl logs <pod> --tail=200`, `kubectl get events --sort-by=.lastTimestamp`,
  and probe with `curl` from the toolbox.
- Custom `toolbox.image` in the slice can bundle kubot for in-pod diagnosis:
  `kubox exec <env> -- kubot inspect` (kubot supports in-cluster config).

## Gotchas

- The toolbox SA is bound to namespace-scoped `edit` — scale/delete/patch work inside the
  environment; nodes, namespaces, PVs are out of reach (by design).
- `exec` is one-shot, non-interactive. Use `sh -c '...'` for pipes/loops.
- MCP users: set the kubox server timeout to `180000` — load tests can run ~65s and a
  default 10s MCP timeout will kill them.
- Windows: docker/kind/kubectl noise on stderr is normal; exit code is what matters.
- Always leave a TTL. Always `down` when done.
