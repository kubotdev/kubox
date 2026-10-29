# Commands

Global flags (every command): `--kubeconfig`, `--context`, `--timeout`
(total wall-clock budget, default 5 m), `--version`.

| Command | What it does |
|---|---|
| `init <dir>` | Scaffold a slice: `kubox.yaml` + starter `tests/load.js` (`--force` to overwrite) |
| `build <slice>` | `docker build` the `app.build.context` as `app.image`; `kind load` when the context is kind (`--no-load` to skip) |
| `up <slice> --name N --ttl 1h` | Deploy a slice; re-syncs (rolls pods) if the environment exists |
| `down N` | Delete an environment and everything in it (waits until it's gone) |
| `exec N -- <cmd>` | Run a command in the environment's toolbox pod |
| `exec --create N --slice D --delete-after -- <cmd>` | One-shot: create, run, delete |
| `list` | List live environments (name, status, expiry, services) |
| `status N` | One environment's status |
| `test load N --script f.js` | k6 load test against the app endpoint |
| `test load N --report [--json]` | Same, wrapped in the [verification envelope](json.md) |
| `test drill N --dependency d` | Failure drill: restart a dependency, verify the app recovers |
| `test drill N --all` | Drill every dependency in the environment |
| `mcp` | MCP server on stdio (see [MCP](mcp.md)) |

`test load` and `test drill` accept the one-shot set: `--create --slice
<dir> --ttl <dur> --delete-after`, so a full verify cycle is one command.

## Flags that matter

- `up`: `--name` (required), `--ttl` (required mindset — see below).
- `test load`: `--script` (required), `--report` for the envelope,
  `--json` for machines.
- `test drill`: `--dependency` or `--all`, `--type` (default
  `dependency-restart`), `--timeout` (per-phase budget, default 2 m),
  `--json`.

## Exit codes

A scriptable contract:

| Code | Meaning |
|---|---|
| 0 | Success (for `test`: every check passed) |
| 1 | Operation failed — for `test`: a check **failed**, environment healthy |
| 2 | Not authenticated / no kubeconfig |
| 3 | Environment unhealthy — verification **checks not run** |
| 64 | Bad usage |

For agents the 1-vs-3 split is the whole game: **1 means fix the app, 3
means fix the environment** (image, quota, crashlooping dependency) and
never the app code. The same split appears as `status: fail|error` in the
[envelope](json.md).

## TTL is not optional

Every environment carries `kubox.dev/expires-at`. Always pass `--ttl`
(default `1h`) and delete explicitly when done — the TTL is the backstop
for crashed agents, not the plan.
