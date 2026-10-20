<h1 align="center">kubox</h1>

<p align="center">
  <strong>Disposable Kubernetes environments for AI agents.</strong><br>
  Agents write your code — kubox makes them <em>prove</em> it.<br>
  A cluster slice per agent: quota-bound, network-isolated, TTL'd, load-testable.
</p>

<p align="center">
  <a href="https://github.com/kubotdev/kubox/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/kubotdev/kubox/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/kubotdev/kubox/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/kubotdev/kubox"></a>
  <a href="LICENSE"><img alt="License: Apache-2.0" src="https://img.shields.io/badge/license-Apache--2.0-blue"></a>
</p>

<p align="center">
  <a href="#quickstart">Quickstart</a> ·
  <a href="#install">Install</a> ·
  <a href="#define-a-slice">Slices</a> ·
  <a href="#test-infrastructure-not-just-traffic">Testing</a> ·
  <a href="#getting-images-into-the-environment">Images</a> ·
  <a href="#use-it-with-your-agent">Agents</a> ·
  <a href="#commands">Commands</a> ·
  <a href="#mcp">MCP</a> ·
  <a href="#roadmap">Roadmap</a>
</p>

> **Status: early (v0.1).** Sharp edges expected; the CLI and MCP tool surface
> may change. kubox does not build or push images — see
> [Getting images into the environment](#getting-images-into-the-environment).

---

## Why

Agents generate code faster than anyone can review it. The missing loop is
**verification**: something has to actually run the change under load and say
pass/fail — in a place where breaking things is safe.

kubox is that place. One command (or one MCP call) deploys your app plus its
immediate neighbors into a disposable namespace with quotas, default-deny
network policy, and a TTL. Your agent load-tests it, breaks it, fixes it,
re-syncs, re-tests — then it's gone.

**kubot** tells you what's wrong with your infrastructure. **kubox** gives
agents a safe place to make it right, and a pass/fail signal they can read.

## Quickstart

```sh
curl -fsSL https://raw.githubusercontent.com/kubotdev/kubox/main/install.sh | sh
kind create cluster              # skip if you already have a cluster
kubox up ./myapp.kubox --name demo --ttl 1h
kubox test load demo --script tests/load.js
kubox down demo
```

No cluster handy? `kind` gives you one in ~30 s, and kubox works against any
cluster your kubeconfig can reach (kind, EKS, GKE, minikube, …).

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/kubotdev/kubox/main/install.sh | sh
```

Or build from source (needs Go):

```sh
go install github.com/kubotdev/kubox/cmd/kubox@latest
```

The script downloads the release archive for your OS/arch from GitHub
Releases (Linux/macOS `.tar.gz`, Windows `.zip`), verifies its sha256
checksum always, and the cosign signature (keyless, GitHub Actions OIDC)
when `cosign` is on PATH. Pin a version with `KUBOX_VERSION=v0.1.0`,
change the destination with `KUBOX_INSTALL_DIR`. To re-install or upgrade,
re-run the same command. `KUBOX_REQUIRE_SIGNATURE=1` makes the signature
mandatory:

```sh
KUBOX_REQUIRE_SIGNATURE=1 curl -fsSL https://raw.githubusercontent.com/kubotdev/kubox/main/install.sh | sh
```

Release binaries report their version via `kubox --version`; local
`go build` reports `dev`.

## Prerequisites

- **A Kubernetes cluster** and a kubeconfig that can reach it (kubox resolves
  it exactly like kubectl: `--kubeconfig`, `$KUBECONFIG`, `~/.kube/config`,
  plus `--context`). [kind](https://kind.sigs.k8s.io/) is the easiest local
  option.
- **A dynamic storage class** if you use `volume` in a slice (kind's default
  `standard` class works out of the box). Without volumes, nothing special is
  needed.
- **Docker** (or compatible) on the machine running kubox, to build the
  images your slice references — kubox never builds or pushes.
- **metrics-server** if you use `scaling` (or want CPU-based HPA tests).
  kind: `kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml`

## How it works

You define a **slice** — a `kubox.yaml` describing the app under test, its
immediate in-cluster neighbors, and resource quotas. kubox deploys that slice
into a disposable namespace with:

- **Resource quotas** — one agent cannot starve the cluster
- **Network policies** — default deny-all, only declared traffic allowed
- **TTL** — environments expire automatically; a crashed agent cannot leak one
- **Service account** — scoped permissions, no cluster-admin

The agent then runs whatever it wants against that environment: k6 load
tests, kubectl, curl, custom scripts. kubox handles the lifecycle; the agent
brings the intelligence.

## Define a slice

```yaml
# myapp.kubox/kubox.yaml
name: payment
app:
  image: myregistry/payment-service:latest
  port: 8080
  env:
    INVENTORY_URL: http://inventory:8080
  healthcheck:
    path: /healthz          # readiness + liveness probes
  resources:                # requests default to 50m/64Mi
    limitsCPU: "1"
    limitsMemory: 512Mi
  scaling:                  # optional HPA for the app
    min: 2
    max: 8
    cpuPercent: 70
dependencies:
  - name: inventory
    image: myregistry/inventory:latest
    port: 8080
  - name: postgres
    image: postgres:16-alpine
    port: 5432
    env:
      POSTGRES_PASSWORD: kubox
    volume:
      size: 1Gi
      path: /var/lib/postgresql/data
quotas:
  cpu: "2"
  memory: 4Gi
  pods: "20"
```

Quotas are namespace-wide, so size them to cover the app, its dependencies,
*and* kubox's own pods: the toolbox (50m/64Mi) and the k6 test runner
(100m/64Mi). If `cpu`/`memory` headroom is too tight, pod creation fails
with `failed quota`.

`volume` gives a dependency a persistent disk (PVC) mounted at `path`, so its
data survives pod restarts and re-syncs — use it for databases. The PVC is
deleted together with the environment.

`toolbox.image` (default `alpine/k8s:1.30.2`) swaps the toolbox pod's image —
use it to bring extra tools (e.g. kubot) into the environment:

### Environments are re-syncable

Run `kubox up` again with the same name to update a live environment from the
slice: resources are applied, the TTL is refreshed, pods roll so a rebuilt
image is picked up, removed services are pruned, and the command returns when
every deployment is ready.

### Health checks, resources, scaling

- `healthcheck.path` adds readiness **and** liveness HTTP probes — "ready"
  then means *the app answers*, not just *the pod is Running*. Liveness gets
  10 extra seconds of initial delay so restarts don't race startup.
- `resources` (`requestsCPU`/`requestsMemory` default 50m/64Mi; `limitsCPU`/
  `limitsMemory` optional) set per-container requests and limits. Tight limits
  let you test CPU throttling and OOMKill recovery.
- `scaling` turns the `replicas` field into a managed HPA on the app
  (min/max + average CPU target, default 70%). Re-syncs won't fight the HPA
  for the replica count; removing `scaling` from the slice deletes the HPA.

## Test infrastructure, not just traffic

Load tests are the beginning. The environment is a full Kubernetes namespace,
so an agent (or you) can run the drills that used to need a staging cluster:

```sh
# recovery: kill the app, watch it come back through its probe
kubox exec demo -- kubectl delete pod -l app=payment
kubox exec demo -- kubectl get pods -w

# dependency restart: data survives via the PVC
kubox exec demo -- kubectl delete pod -l app=postgres

# manual horizontal scaling under load
kubox exec demo -- kubectl autoscale deployment payment --min=2 --max=10 --cpu-percent=50
kubox test load demo --script load.js
kubox exec demo -- kubectl get hpa -w

# resource pressure: set tight limits in the slice, re-sync, crush,
# and watch throttle/OOMKill + restart
kubox exec demo -- kubectl get pods -w
```

Diagnosing failures: if **[kubot](https://github.com/kubotdev/kubot)** is
installed, run it against the environment's namespace (`kubot inspect -n
kubox-demo`) for a read-only, findings-first report — it resolves the same
kubeconfig. Without kubot, everything you need is already in the toolbox:
`kubox exec demo -- kubectl describe pod <pod>`, `kubectl logs <pod>`, or
`kubot` installed *inside* the environment via a custom `toolbox.image` in
the slice (kubot supports in-cluster config).

`scaling` needs [metrics-server](https://github.com/kubernetes-sigs/metrics-server)
on the cluster (kind doesn't ship it):

```sh
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

## Getting images into the environment

kubox deploys the images you reference; it never builds or pushes them.

**Local kind cluster** — build and load:

```sh
docker build -t payment-service:v1 ./payment-service
kind load docker-image payment-service:v1 --name <cluster-name>
```

**Remote cluster** — push to a registry the cluster can pull from
(`docker push …`, then reference that tag in the slice).

After changing code: rebuild → load/push → `kubox up` again. The re-sync
rolls the pods, and your environment runs the new version in seconds.

## Create, test, clean up

```sh
kubox up myapp.kubox --name fix-auth --ttl 1h
kubox exec fix-auth -- kubectl get pods
kubox exec fix-auth -- curl http://payment:8080/healthz
kubox test load fix-auth --script load.js
kubox down fix-auth
```

Or the one-shot form that creates, runs, and deletes in one command:

```sh
kubox exec --create fix-auth --slice myapp.kubox --ttl 1h --delete-after -- \
  k6 run load.js
```

## Load tests are pass/fail signals

```sh
kubox test load fix-auth --script tests/load.js
```

The k6 script's `BASE_URL` is set automatically. Thresholds in the script
become the pass/fail signal the agent (or CI) acts on:

```javascript
export const options = {
  stages: [
    { duration: '30s', target: 20 },
    { duration: '1m', target: 20 },
    { duration: '10s', target: 0 },
  ],
  thresholds: {
    http_req_duration: ['p(95)<200'],
    http_req_failed: ['rate<0.01'],
  },
};
export default function () {
  http.get(`${__ENV.BASE_URL}/healthz`);
}
```

For agents and CI, `--json` returns structured results:

```sh
kubox test load --create fix-auth --slice myapp.kubox --ttl 1h \
  --delete-after --script load.js --json
```

```json
{
  "status": "pass",
  "exit_code": 0,
  "thresholds": {
    "http_req_duration_p95_lt_200": { "value": 142.3, "pass": true },
    "http_req_failed_rate_lt_0.01": { "value": 0.002, "pass": true }
  },
  "summary": {
    "requests": 15234,
    "rps": 124.7,
    "p50": 88.1,
    "p95": 142.3,
    "p99": 198.2
  }
}
```

## Use it with your agent

kubox ships a skill that teaches coding agents the judgment layer — the
break → fix → prove loop, plus load, recovery, scaling, and resource-pressure
playbooks, with hard rules (always TTL + teardown, thresholds are the
contract, diagnose before fixing):

```bash
# any Skills-aware agent (Claude Code, Cursor, opencode, …)
curl -fsSL https://raw.githubusercontent.com/kubotdev/kubox/main/skills/install.sh | sh

# or from a checkout
cp -r skills/kubernetes-verification ~/.claude/skills/       # Claude Code
cp -r skills/kubernetes-verification .opencode/skills/       # opencode (project)
```

Details: [`skills/README.md`](skills/README.md).

For the full tool surface (create, exec, test, delete from inside the
session), also wire up the MCP server below.

## MCP

kubox runs as an MCP server so coding agents (Claude Code, Cursor, opencode,
…) can drive the full loop — create, test, fix, re-sync, verify, tear down:

```json
{
  "mcpServers": {
    "kubox": {
      "command": "kubox",
      "args": ["mcp"],
      "env": { "KUBECONFIG": "/path/to/kubeconfig" }
    }
  }
}
```

opencode (`opencode.json`) uses a slightly different shape — and bump the
timeout if you run load tests through it (k6 runs can exceed the default):

```json
{
  "mcp": {
    "kubox": {
      "type": "local",
      "command": ["kubox", "mcp"],
      "enabled": true,
      "timeout": 180000
    }
  }
}
```

Tools: `up`, `down`, `exec`, `list`, `status`, `test_load`.

## Commands

| Command | What it does |
|---|---|
| `kubox up <slice> --name N --ttl 1h` | Deploy a slice; re-syncs (rolls pods) if the environment exists |
| `kubox down N` | Delete an environment (waits until it's gone) |
| `kubox exec N -- <cmd>` | Run a command in the environment's toolbox |
| `kubox exec --create N --slice D --delete-after -- <cmd>` | One-shot: create, run, delete |
| `kubox list` | List live environments |
| `kubox status N` | Show one environment's status |
| `kubox test load N --script f.js` | Run a k6 load test (`--json` for machines) |
| `kubox mcp` | Run as an MCP server |

## Roadmap

kubox stays a **lifecycle + guardrails + measurement** layer — it will not
grow a build system, a registry, or a UI. What's next, roughly:

- **v0.2** — `kubox init` slice scaffold, `kubox build` (docker + `kind load`
  convenience), a committed example app, an official toolbox image
  (`kubotdev/kubox-toolbox`: kubectl + k6 + kubot for in-environment diagnosis)
- **v0.3** — CI/PR one-shots and a GitHub Action (the `--create
  --delete-after --json` contract already exists)

Feedback and issues welcome — this is early, and direction is still being
shaped.

## License

[Apache-2.0](LICENSE).
