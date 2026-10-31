# Testing

Agents write code faster than anyone can review it. The missing loop is
verification: run the change under load and under failure, in a place where
breaking things is safe, and get pass/fail back. That's what kubox's test
layer does. kubox doesn't make testing *smart* — the thresholds live in
your k6 script, the intelligence lives in the agent — it makes the
evidence **standardized and trustworthy**.

Two check types today: load ([k6](#load-tests)) and drills
([dependency-restart](#drills), [scale-under-load](#scale-under-load)).
`--kubot` adds a [diagnosis](#diagnosis) check to either.

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

The test is **agent-owned**: it lives in the slice (`tests/load.js`), the
agent writes it — endpoints from the slice's healthcheck, thresholds
state the SLO — and with `--slice` kubox defaults to that slot (missing
file → exit 64; a run without thresholds isn't verification).

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

### `scale-under-load`

With `app.scaling` in the slice, verifies the HPA actually works when
traffic arrives — and that the SLO holds while it scales:

```sh
kubox test drill fix-auth --type scale-under-load --script load.js --json
```

The k6 script runs for the drill's duration; the drill watches the
deployment, each phase timed:

| Phase | Pass criterion |
|---|---|
| `baseline` | the HPA reports `ScalingActive` — fails with a metrics-server hint when it doesn't |
| `scale_up` | replicas exceed the baseline *during* the load |
| `load_slo` | the k6 script's own thresholds all pass (they are the assertion) |
| `settle` | steady state: app still healthy |

Preflight catches the two setup mistakes before running: no `scaling` block
→ `error` with "add `app.scaling`", `min == max` → `error` with "raise
`app.scaling.max`". Scale-down is never asserted — the HPA's default
stabilization window is five minutes, so waiting for it in CI is noise.

### More drills later

The slice is the fault model; drills are derived from declared fields, not
discovered:

| Slice declares | Drill (planned) | Verifies |
|---|---|---|
| `dependencies[].volume` | `volume-survives` | data survives the restart |
| `app.resources` limits | `resource-pressure` | throttle/OOMKill handling |

### Drill safety

- Only ever touches pods carrying `kubox.dev/env=<name>` inside the
  environment's own namespace; `--dependency` must name a declared service.
- The environment is preflighted first — an unhealthy sandbox gets no fault
  injected and the report says `error` (exit 3), never a misleading `fail`.
- A settle phase returns the environment to steady state, so a following
  load test can't fail spuriously against a still-recovering dependency.

## Diagnosis

If **[kubot](https://github.com/kubotdev/kubot)** is available, `--kubot`
folds its read-only inspection of the environment's namespace into the
envelope as a `diagnosis` check:

```sh
kubox test load fix-auth --script load.js --report --kubot --json
kubox test drill fix-auth --all --kubot --json
```

- Runs `kubot inspect -n <namespace> --json` inside the toolbox (no kubot
  install on the runner needed).
- The check fails only on non-suppressed **`critical`** findings;
  warnings/notes are reported, never gating.
- kubot missing or output unparseable → `skipped` (with the reason), so an
  absent tool never fails a run.
- The parsed kubot report rides in `checks[].detail`; suppressed issues
  never gate. The check is appended only when the environment is healthy.

`test load --kubot` requires `--report` — a bare k6 result has nowhere to
put it.

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
