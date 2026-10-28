# JSON contract

`--json` on `test load --report` and `test drill` emits the **verification
envelope** — the interface to build on. Terminal output is unstable by
design; parse `--json`, never the text.

The envelope answers the question a single test type can't: did the app
under test fail, or is the sandbox itself broken?

```json
{
  "status": "fail",
  "environment": { "healthy": true, "namespace": "kubox-fix-auth", "problems": [] },
  "checks": [
    {
      "type": "load",
      "status": "fail",
      "duration_ms": 103422,
      "findings": [
        {
          "metric": "http_req_duration_p95_lt_200",
          "expected": "pass",
          "actual": "347",
          "message": "threshold http_req_duration_p95_lt_200 not met (value 347)"
        }
      ],
      "detail": { "status": "fail", "exit_code": 1, "thresholds": {} }
    }
  ]
}
```

## Envelope

- **Report**: `status` (`pass|fail|error`), `environment`
  ([EnvHealth](#environment-health)), `checks[]`.
- **Check**: `type` (`load`, `drill:dependency-restart`), `status`
  (`pass|fail|error|skipped`), `duration_ms`, `findings[]`, `detail`.
- **Finding**: `metric`, `expected`, `actual`, `message` — one entry per
  crossed threshold or failed drill phase. Enough for an agent to act
  without opening `detail`.

Status roll-up: any check `error` → `error`; else any `fail` → `fail`; else
`pass`. An unhealthy environment forces `error` regardless of checks.

## Environment health

`environment.healthy` is a preflight over the namespace, run *before* any
check (and before any fault injection). `problems[]` entries carry
`component` (the slice service, when attributable), `kind`, `message`:

| Kind | Means |
|---|---|
| `CrashLoopBackOff`, `ImagePullBackOff`, `ErrImagePull`, … | a service's container is verifiably broken |
| `NotReady` | a deployment is below its replica count (no deeper root cause found) |
| `FailedQuota`, `Unschedulable` | a pod couldn't be created (quota/scheduling) |
| `NoService`, `NoEndpoints` | the app isn't reachable inside the environment |
| `NotFound` | the environment doesn't exist |

An unhealthy environment means checks are **not run** — the report carries
empty `checks[]` and `status: "error"`. This is the distinction no raw test
output gives you: an agent must never "fix" app code because a dependency
was crashlooping under the test.

## `detail`: tool-native, embedded verbatim

The envelope is stable; `detail` is the underlying tool's own output, for
when you want depth:

- **load** — the full k6 result, exactly what `kubox test load --json`
  returns: `status`, `exit_code`, `thresholds` (per-threshold value +
  pass), `summary` (requests, rps, p50/p95/p99).
- **drill** — `target`, `fault` (`delete-pods`), and `phases[]`
  (`name`, `status`, `duration_ms`, `message`) — the per-phase timeline.

Treat `detail` as informational: its shape follows the tool (k6 versions,
new drill types) and may change without notice. The envelope won't.

## Stability

Additive only: existing fields never change meaning or type. `findings[]`
ordering is not part of the contract — sort by `metric` if you need
determinism. `status` and the [exit codes](commands.md#exit-codes) always
agree: `pass`↔0, `fail`↔1, `error`↔3, so shell (`if kubox …; then`) and
JSON consumers get the same trichotomy.
