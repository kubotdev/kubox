# kubox skills

Agent **skills** that teach an AI assistant how to use kubox *well* — the
judgment layer on top of the tools: always TTL + teardown, thresholds are the
contract, re-sync don't recreate, diagnose before fixing, report before/after.

A skill pairs naturally with the kubox **MCP server** (`kubox mcp`): MCP gives
the agent the lifecycle tools, the skill gives it the playbook.

## `kubernetes-verification`

Turns "prove it works in k8s" into a disposable environment, a failing load
test, a root cause, a fix, and a passing test — with the numbers to show for it.

### Install

Works in **Claude Code, Cursor, opencode, Codex** (and any Skills-aware agent).
The [`skills` CLI](https://github.com/vercel-labs/skills) detects which agents
you use and installs to the right place for each:

```bash
npx skills add kubotdev/kubox
```

Or the kubox one-liner (Claude Code / Skills-aware clients):

```bash
curl -fsSL https://raw.githubusercontent.com/kubotdev/kubox/main/skills/install.sh | sh
```

It drops the skill at `~/.claude/skills/kubernetes-verification/SKILL.md`
(override with `KUBOX_SKILL_DIR`, e.g. `.claude/skills` or `.opencode/skills`
for a project-scoped install). Or copy from a checkout:

```bash
cp -r skills/kubernetes-verification ~/.claude/skills/
```

Pair it with the MCP server so the agent has both the tools and the playbook:

```bash
claude mcp add kubox -e KUBECONFIG="/path/to/kubeconfig" -- kubox mcp
```

Then ask your agent *"prove my service survives being deployed to k8s"* — it
invokes the skill, spins up an environment, breaks it, fixes it, and reports
before/after numbers.

> The `SKILL.md` format is a small frontmatter block (`name`, `description`)
> followed by the instructions. `description` is what the agent matches on to
> decide the skill is relevant, so it names the situations kubox is for.
