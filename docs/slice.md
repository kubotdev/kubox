# The slice

A slice is a directory with a `kubox.yaml` describing the app under test,
its **immediate** in-cluster neighbors, and the resource envelope. It's the
one thing a human writes (once); everything else — environments, drills,
load tests — derives from it.

```yaml
name: payment
app:
  image: myregistry/payment-service:latest
  port: 8080
  env:
    INVENTORY_URL: http://inventory:8080
  healthcheck:
    path: /healthz
  resources:
    limitsCPU: "1"
    limitsMemory: 512Mi
  scaling:
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

## Fields

| Field | Required | Default | Notes |
|---|---|---|---|
| `name` | yes | — | environment/service name prefix |
| `app.image` | yes | — | the tag `kubox build` produces and `up` deploys |
| `app.build.context` | no | — | directory (relative to the slice) with the Dockerfile; enables `kubox build` |
| `app.build.dockerfile` | no | `Dockerfile` | Dockerfile name inside the context |
| `app.port` | no | `8080` | container + service port |
| `app.replicas` | no | `1` | ignored while `scaling` is set (HPA owns it) |
| `app.env` | no | — | plain env vars |
| `app.healthcheck.path` | no | — | must start with `/`; adds readiness **and** liveness HTTP probes (liveness gets +10 s initial delay) |
| `app.resources` | no | requests `50m`/`64Mi` | `requestsCPU/requestsMemory/limitsCPU/limitsMemory`; tight limits let you test throttle/OOMKill |
| `app.scaling` | no | — | managed HPA: `min` (≥1, default 1), `max` (required), `cpuPercent` (default 70) |
| `dependencies[]` | no | — | each needs `name` + `image`; same `env`/`resources` as app; single replica |
| `dependencies[].volume` | no | — | `size` + `path`: a PVC mounted at `path`; survives re-syncs and pod restarts, deleted with the environment |
| `quotas` | no | cpu `2`, memory `4Gi`, pods `20` | namespace-wide — see below |
| `networkPolicy.default` | no | `deny-all` | default-deny all ingress/egress except intra-slice traffic + DNS + API server |
| `toolbox.image` | no | `ghcr.io/kubotdev/kubox-toolbox:latest` | the toolbox pod's image (kubectl + k6 + kubot + shell tools); swap for your own |

## Quotas are namespace-wide

Size `cpu`/`memory`/`pods` to cover the app, its dependencies, **and**
kubox's own pods: the toolbox (50m/64Mi) and the k6 runner (100m/64Mi).
Too tight and pod creation fails with `failed quota`.

## What gets cloned

The slice is a cut through the dependency graph, not the whole graph:

- **The app + its immediate neighbors** (`dependencies[]`) — deployed for
  real into the environment namespace. Stateful ones too: a real Postgres
  with a PVC is how you catch reconnect bugs a mock would hide.
- **Everything else** (external SaaS, other teams' services, shared
  databases) — stays wherever it is, or gets mocked in your app's config.
  kubox does not auto-discover the service graph; the human decides.

## Re-sync is the update path

`kubox up` again with the same name updates the live environment in place:
resources applied, TTL refreshed, pods rolled (new image picked up),
removed services pruned, returns when everything is Ready. Data volumes
survive. Delete only when you want a from-scratch state.

## Deriving drills

The slice doubles as a fault model. Declared fields map to verification
drills — a dependency can be restarted, a volume-bearing one can be checked
for data survival, `scaling` can be validated under load. See
[Testing](testing.md).
