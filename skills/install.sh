#!/bin/sh
# kubox skill installer. Downloads the kubernetes-verification SKILL.md into your
# agent's skills directory so it knows how to use kubox well.
#
#   curl -fsSL https://raw.githubusercontent.com/kubotdev/kubox/main/skills/install.sh | sh
#
# Env:
#   KUBOX_SKILL      skill name to install (default: kubernetes-verification)
#   KUBOX_SKILL_DIR  target skills dir    (default: ~/.claude/skills)
set -eu

REPO="kubotdev/kubox"
SKILL="${KUBOX_SKILL:-kubernetes-verification}"
SKILL_DIR="${KUBOX_SKILL_DIR:-$HOME/.claude/skills}"

say() { printf 'kubox-skill: %s\n' "$1" >&2; }
die() { say "error: $1"; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required"

src="https://raw.githubusercontent.com/$REPO/main/skills/$SKILL/SKILL.md"
dest="$SKILL_DIR/$SKILL"

say "installing '$SKILL' skill to $dest"
mkdir -p "$dest" || die "cannot create $dest"
curl -fsSL "$src" -o "$dest/SKILL.md" || die "download failed ($src)"

# Sanity check: a real skill starts with YAML frontmatter.
head -n1 "$dest/SKILL.md" | grep -q '^---' || die "downloaded file is not a SKILL.md"

say "installed $dest/SKILL.md"
say "start a new agent session to pick it up. Pair it with the tools:"
say "  claude mcp add kubox -e KUBECONFIG=\"…\" -- kubox mcp"
