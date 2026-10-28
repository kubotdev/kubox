# Agents

kubox is built for the loop an AI coding agent runs: implement → verify →
fix → verify — fast, and without a human babysitting the environment. Two
pieces make that work: the **skill** (the judgment layer) and **MCP** (the
tool surface).

## The skill

```sh
# any Skills-aware agent (Claude Code, Cursor, opencode, …)
curl -fsSL https://raw.githubusercontent.com/kubotdev/kubox/main/skills/install.sh | sh

# or from a checkout
cp -r skills/kubernetes-verification ~/.claude/skills/       # Claude Code
cp -r skills/kubernetes-verification .opencode/skills/       # opencode (project)
```

kubox provides the environment; the skill teaches the *judgment*: the
break → fix → prove loop, playbooks for load, recovery, scaling, and
resource pressure, and hard rules — always TTL + teardown, thresholds are
the contract (fix the code, never the threshold), diagnose before fixing,
and **read the failure kind** (`error`/exit 3 = environment problem, don't
touch app code; `fail`/exit 1 = your change lost).

Details: [`skills/README.md`](../skills/README.md).

## The workflow

The human sets the stage, once:

- write the [slice](slice.md) — the app, its neighbors, quotas
- optionally write the k6 threshold template (or let the agent draft one)

The agent performs, per task:

| Step | Command |
|---|---|
| create | `kubox up <slice> --name task-42 --ttl 30m` (or `--create`) |
| verify under load | `kubox test load task-42 --script load.js --report --json` |
| verify against failure | `kubox test drill task-42 --all --json` |
| fix | edit code → rebuild image → `kubox up` again (re-sync rolls pods) |
| prove | re-run the checks — `pass` plus the findings is the evidence |
| destroy | `kubox down task-42` (or `--delete-after`; TTL is the backstop) |

The [envelope](json.md) is what closes the loop autonomously: `fail` tells
the agent which metric missed, `error` tells it to look at the environment
instead of the code. No parsing of terminal prose, no guessing.

## MCP for the full surface

The skill teaches judgment; the MCP server gives the agent hands. With
`kubox mcp` wired up, the agent creates, execs into, tests, and deletes
environments from inside its own session — see [MCP](mcp.md). Bump the MCP
timeout for load tests; k6 runs exceed client defaults.

## Concurrency

Environments are namespaced per name (`kubox-<name>`), quota-bound, and
TTL'd — parallel agents on different tasks don't interfere. Keep task names
deterministic (branch/PR id), always set a TTL, and let each agent clean up
after itself. A crashed agent leaks nothing past the TTL.
