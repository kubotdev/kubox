# Testing

Agents write code faster than anyone can review it. The missing loop is
verification: run the change under load and under failure, in a place where
breaking things is safe, and get pass/fail back. That's what kubox's test
layer does. kubox doesn't make testing *smart* — the thresholds live in
your k6 script, the intelligence lives in the agent — it makes the
evidence **standardized and trustworthy**.

Two check types today: load ([k6](#load-tests)) and drills
([dependency-restart](#drills)).

## The loop

```
create → break → fix → prove → destroy
```

1. `kubox up` (or `--create`) — isolated, quota-bound environment
2. break it: load pressure (`test load`), failure injection (`test drill`)
3. the agent reads the finding, fixes the code, re-syncs (`up` again)
4. re-run the check — the evidence says pass or names the exact miss
5. `kubox down` (or `--delete-after`, or the TTL)

## Load tests

```sh
kubox test load fix-auth --script tests/load.js --report --json
```

The k6 script's `BASE_URL` is set automatically to the app's in-cluster
DNS name. Thresholds in the script are the contract:

```javascript
export const options = {
  stages: [{ duration: '30s', target: 20 }, { duration: '1m', target: 20 }],
  thresholds: {
    http_req_duration: ['p(95)<200'],
    http_req_failed: ['rate<0.01'],
  },
};
export default function () {
  http.get(`${__ENV.BASE_URL}/healthz`);
}
```

k6 evaluates the thresholds and exits 0/1; kubox wraps that into the
[verification envelope](json.md). A FAIL means **fix the code, not the
threshold** — a run you loosened until it passed proves nothing.

## Drills

Load tests probe traffic. Drills probe *failure* — the faults production
throws at you that a perfect-world test never sees. The slice already
declares your app's dependency surface, so kubox runs the drills
mechanically:

```sh
kubox test drill fix-auth --dependency postgres --json
kubox test drill fix-auth --all          # every declared dependency
```

### `dependency-restart`

Deletes the dependency's pods (UID-preconditioned, graceful) and verifies
recovery **on both sides**, each phase timed:

| Phase | Pass criterion |
|---|---|
| `inject` | the dependency's pod(s) were deleted |
| `dependency_ready` | a *replacement* pod reaches Ready |
| `app_recovered` | the app answers its health probe again (HTTP when the slice declares `healthcheck.path`, TCP otherwise) |
| `app_replicas_ready` | the app returns to full replica capacity |
| `settle` | a steady-state window: app still healthy, dependency still up |

The observation order is the diagnosis. Both sides are measured, so the
finding names where recovery broke:

- dependency slow to restart → the dependency itself is broken (image,
  data migration, crashloop) — look at its pods
- dependency fine, app never answers → **the app never reconnected**:
  missing retry/reconnect logic, stale connection pool — the classic
  SYN_SENT-after-restart bug a mock would have hidden
- app answers but replicas don't refill → probe/deployment problem

After the first failed or timed-out phase, the remaining ones are marked
`skipped` — no point measuring replicas while the dependency is still down.

### More drills later

The slice is the fault model; drills are derived from declared fields, not
discovered:

| Slice declares | Drill (planned) | Verifies |
|---|---|---|
| `dependencies[].volume` | `volume-survives` | data survives the restart |
| `app.resources` limits | `resource-pressure` | throttle/OOMKill handling |
| `app.scaling` | `scale-under-load` | HPA scales, SLO holds |

### Drill safety

- Only ever touches pods carrying `kubox.dev/env=<name>` inside the
  environment's own namespace; `--dependency` must name a declared service.
- The environment is preflighted first — an unhealthy sandbox gets no fault
  injected and the report says `error` (exit 3), never a misleading `fail`.
- A settle phase returns the environment to steady state, so a following
  load test can't fail spuriously against a still-recovering dependency.

## Reading results

Terminal output is for humans; parse `--json` (see the [JSON
contract](json.md)). The decision table:

| You see | Means | Do |
|---|---|---|
| `pass` / exit 0 | verified | report done, with the evidence |
| `fail` / exit 1 | your change lost a check it actually ran | fix code, re-sync, re-run |
| `error` / exit 3 | environment broken, checks didn't run | fix env/slice — never app code |

Then destroy: `down`, `--delete-after`, or the TTL — and never leave an
environment without one.
