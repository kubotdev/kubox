# Troubleshooting

## `ImagePullBackOff` after `up`

The image isn't in the cluster. kubox never builds or pushes — that's
yours: `docker build`, then `kind load docker-image <img> --name <cluster>`
for kind, or `docker push` to a registry the cluster can pull from. `up`
again to re-sync; the pods roll to the new image.

## Pods Pending / `failed quota`

The namespace quota is too tight. It must cover the app, its dependencies,
**and** kubox's own pods: the toolbox (50m/64Mi) and the k6 runner
(100m/64Mi). Raise `quotas` in the slice and re-sync. The verification
envelope reports this as `FailedQuota`/`Unschedulable` in
`environment.problems` (status `error`, exit 3) — not as a test failure.

## HPA doesn't move under load

metrics-server is missing. Most managed clusters ship it; kind doesn't:

```sh
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

## `status: "error"` vs `status: "fail"`

Not a bug — the contract. `fail` (exit 1) means the environment was healthy
and your change lost a check it actually ran: fix the code. `error` (exit
3) means the environment was already broken (crashlooping dependency,
quota, no endpoints) and checks were **not run**: fix the environment,
never the app code. `environment.problems[]` names the culprit.

## Drill: `dependency_ready` timed out

The dependency never came back after the restart — the drill stops there.
Look at the dependency itself: `kubox exec <env> -- kubectl describe pod
-l app=<dep>` and its logs. Usually the image, a data migration that
crashes on start, or credentials.

## Drill: `app_recovered` timed out but the dependency recovered

That's the drill working as intended — this is your app failing to
reconnect: missing retry logic, a connection pool that never resets, a
client that cached a dead address. Classic SYN_SENT-after-restart. If the
app *shouldn't* depend on the restart at all, check the probe: the drill
uses HTTP when the slice declares `app.healthcheck.path`, TCP otherwise —
a wrong path looks like a never-recovering app.

## Environments outlive their TTL (until the next command)

TTL enforcement is sweep-on-invocation: **every** kubox command sweeps
expired environments first. If nothing runs kubox at all, an expired
environment lingers until the next invocation or manual `down`. Fine while
agents are working; if you go hands-off for days, `down` explicitly.

## MCP load tests / drills die early

Client default timeouts (~10–60 s) are shorter than a k6 run. Raise the MCP
server's `timeout` (opencode: `180000` in `opencode.json`; Claude/Cursor:
the client's MCP timeout setting). `kubox test` itself budgets 15 m unless
you pass `--timeout`.

## PVC stuck Pending

`volume` needs a dynamic storage class. kind's default `standard` works
out of the box; on clusters without one, either configure a storage class
or drop the `volume` block.

## `kubox up` says the namespace is still being deleted

Kubernetes finalizers can take a minute on namespaces with many pods.
kubox waits, then asks you to retry shortly. If it hangs for real:
`kubectl get ns kubox-<name>` and check what's blocking termination.
