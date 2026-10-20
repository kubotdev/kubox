# kubox

Disposable Kubernetes environments for AI agents.

**kubot** tells you what's wrong with your infrastructure. **kubox** gives agents the infrastructure they need to work, test, and verify their changes safely.

Every agent gets its own Kubernetes slice — isolated, quota-bound, network-isolated, and disposable. Ready in seconds, gone when the TTL expires.

## Install

```sh
curl -fsSL {url} | sh
```

Or build from source:

```sh
make install
```

## How it works

You define a **slice** — a `kubox.yaml` describing the app under test, its immediate in-cluster neighbors, and resource quotas. kubox deploys that slice into a disposable namespace with:

- **Resource quotas** — one agent cannot starve the cluster
- **Network policies** — default deny-all, only declared traffic allowed
- **TTL** — environments expire automatically; a crashed agent cannot leak one
- **Service account** — scoped permissions, no cluster-admin

The agent then runs whatever it wants against that environment: k6 load tests, kubectl, curl, custom scripts. kubox handles the lifecycle; the agent brings the intelligence.

## Quick start

### 1. Define a slice

```yaml
# myapp.kubox/kubox.yaml
name: payment
app:
  image: myregistry/payment-service:latest
  port: 8080
  env:
    INVENTORY_URL: http://inventory:8080
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

Quotas are namespace-wide, so size them to cover the app, its dependencies, *and* kubox's own pods: the toolbox (50m/64Mi) and the k6 test runner (100m/64Mi). If `cpu`/`memory` headroom is too tight, pod creation fails with `failed quota`.

`volume` gives a dependency a persistent disk (PVC) mounted at `path`, so its data survives pod restarts and re-syncs — use it for databases. The PVC is deleted together with the environment.

### 2. Create an environment

```sh
kubox up myapp.kubox --name fix-auth --ttl 1h
```

Run `kubox up` again with the same name to re-sync a live environment: resources are updated from the slice, the TTL is refreshed, pods roll so a rebuilt image is picked up, and the command returns when every deployment is ready.

### 3. Run commands inside it

```sh
kubox exec fix-auth -- kubectl get pods
kubox exec fix-auth -- curl http://payment:8080/healthz
```

### 4. Run a load test (v1)

```sh
kubox test load fix-auth --script load.js
```

The k6 script's `BASE_URL` is set automatically. Thresholds in the script become the pass/fail signal:

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

### 5. Clean up

```sh
kubox down fix-auth
```

Or use the one-shot form that creates, runs, and deletes in one command:

```sh
kubox exec --create fix-auth --slice myapp.kubox --ttl 1h --delete-after -- \
  k6 run load.js
```

## One-shot for agents

```sh
kubox test load --create fix-auth --slice myapp.kubox --ttl 1h \
  --delete-after --script load.js --json
```

Returns structured JSON:

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

## MCP

kubox runs as an MCP server so Claude Code / Cursor can drive the full loop:

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

Tools: `up`, `down`, `exec`, `list`, `status`, `test_load`.

## Commands

| Command | What it does |
|---|---|
| `kubox up <slice> --name N --ttl 1h` | Deploy a slice; re-syncs (rolls pods) if the environment exists |
| `kubox down N` | Delete an environment |
| `kubox exec N -- <cmd>` | Run a command in the environment's toolbox |
| `kubox exec --create N --slice D --delete-after -- <cmd>` | One-shot: create, run, delete |
| `kubox list` | List live environments |
| `kubox status N` | Show one environment's status |
| `kubox test load N --script f.js` | Run a k6 load test |
| `kubox mcp` | Run as an MCP server |
