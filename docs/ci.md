# CI

The composite action runs the full verify loop on a throwaway runner:
provision → build → load test → optional drill → teardown. Same envelope,
same exit codes, no cluster to babysit.

```yaml
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: kubotdev/kubox@v0.4.0
        with:
          slice: myapp.kubox            # uses myapp.kubox/tests/load.js
          drill: dependency-restart:postgres
```

## Inputs

| Input | Default | What it does |
|---|---|---|
| `slice` | — (required) | Slice directory (contains `kubox.yaml`) |
| `script` | `<slice>/tests/load.js` | k6 script — absolute, or relative to the slice (`tests/` searched). Omit to use the convention slot; missing file → exit 64 (the agent writes the test) |
| `name` | `ci` | Environment name (namespace suffix) |
| `cluster` | `kind` | `kind` creates a throwaway cluster; `existing` uses the current kubeconfig context |
| `build` | `true` | `docker build` + `kind load` before `up` (skipped automatically when the slice has no `app.build`) |
| `drill` | `none` | `dependency-restart:<service>`, `volume-survives:<service>`, or `scale-under-load` (kind gets metrics-server installed automatically) |
| `ttl` | `30m` | Environment TTL |
| `kubox-version` | `latest` | kubox release tag to install. `source` builds kubox from this checkout instead (`uses: ./` — dogfood unreleased changes; needs Go on the runner) |

kubox's own CI runs this action against [demo/shop](../demo/shop) with
`kubox-version: source` — the action under test is the action being merged.

## Outputs

| Output | What it is |
|---|---|
| `status` | `pass` \| `fail` \| `error` |
| `report` | Load-test envelope JSON (single-line) |
| `drill-report` | Drill envelope JSON (empty when `drill: none`) |

```yaml
- uses: kubotdev/kubox@v0.4.0
  id: kubox
  with: { slice: myapp.kubox, script: load.js }
- run: echo "${{ steps.kubox.outputs.report }}" | jq .
```

## Exit code

The step's exit code mirrors the envelope: `0` pass, `1` fail (app lost a
check), `3` error (environment unhealthy — checks not run), `64` bad
usage. Branch protections should gate on the step; read `status` when the
next step needs to distinguish fail from error.

## What it does not do

- No registry, no image push — `kind load` only, so `cluster: existing`
  with a remote cluster needs the image available another way.
- No comments or annotations — reporting stays in the step summary and
  outputs.
- Cleanup always runs (`down` is idempotent), so a failed run leaves
  nothing behind even without `--delete-after`.
