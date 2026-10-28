# MCP

`kubox mcp` speaks the Model Context Protocol on stdio, so coding agents
(Claude Code, Cursor, opencode, …) can drive the full loop — create, test,
fix, re-sync, verify, tear down — without a human in the session.

- Tools: `up`, `down`, `exec`, `list`, `status`, `test_load`, `test_drill`.
- Auth is the kubeconfig, nothing else — no tokens in v0.x. Point the
  server's env at the right one.

| Tool | Args | Returns |
|---|---|---|
| `up` | `slice_dir`, `name`, `ttl?` | environment: name, namespace, status, services |
| `down` | `name` | status |
| `exec` | `name`, `command[]` | exit code, stdout, stderr |
| `list` | — | live environments |
| `status` | `name` | one environment's status |
| `test_load` | `name`, `script` | k6 result (thresholds + summary) |
| `test_drill` | `name`, `type?`, `dependency?`, `all?`, `timeout?` | [verification envelope](json.md) |

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

For opencode (`opencode.json`) — and bump the timeout: k6 runs and drills
can exceed the default:

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

Tools are deterministic wrappers over the same Manager the CLI uses —
`test_drill` returns the same envelope as `kubox test drill --json`, with
the same `error`-means-broken-environment semantics. The server's
instructions tell the agent the standing rules: always TTL, kubox never
builds or pushes images, diagnose before fixing.
