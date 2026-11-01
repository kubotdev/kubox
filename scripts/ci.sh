#!/usr/bin/env bash
# kubox GitHub Action driver (inputs: ../action.yml). Installs kubox, creates
# the cluster, provisions the environment, runs the load test (+ optional
# drill), and tears everything down. Exit code follows the kubox contract:
# 0 pass, 1 fail, 3 error (64 = usage).
set -euo pipefail

SLICE="${SLICE:?input 'slice' is required}"
SCRIPT="${SCRIPT:-}"
ENV_NAME="${ENV_NAME:-ci}"
CLUSTER="${CLUSTER:-kind}"
BUILD="${BUILD:-true}"
DRILL="${DRILL:-none}"
TTL="${TTL:-30m}"
KUBOX_VERSION="${KUBOX_VERSION:-latest}"
ACTION_PATH="${KUBOX_ACTION_PATH:-$(cd "$(dirname "$0")/.." && pwd)}"

WORKDIR="${RUNNER_TEMP:-/tmp}/kubox-action"
mkdir -p "$WORKDIR"
export KUBOX_INSTALL_DIR="$WORKDIR/bin"
export PATH="$KUBOX_INSTALL_DIR:$PATH"
LOAD_OUT="$WORKDIR/load-report.json"
DRILL_OUT="$WORKDIR/drill-report.json"
rm -f "$LOAD_OUT" "$DRILL_OUT" # never read a previous run's reports
load_json=""
drill_json=""
KIND_CREATED=""

cleanup() {
  if command -v kubox >/dev/null 2>&1; then
    echo "--- cleanup: kubox down $ENV_NAME"
    kubox down "$ENV_NAME" >/dev/null 2>&1 || true
  fi
  if [ -n "$KIND_CREATED" ]; then
    echo "--- cleanup: kind delete cluster $KIND_CREATED"
    kind delete cluster --name "$KIND_CREATED" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# envelope keeps only envelope-shaped JSON (an object with a string status).
envelope() {
  jq -c 'if type == "object" and (.status | type == "string") then . else empty end' "$1" 2>/dev/null || true
}

summary_report() {
  local title="$1" json="$2"
  [ -n "$json" ] || return 0
  echo "### $title: $(jq -r '.status' <<<"$json")"
  echo ""
  if [ "$(jq -r '.environment.healthy? | tostring' <<<"$json")" = "false" ]; then
    jq -r '.environment.problems[]? | "- [\(.kind)] \(.message)"' <<<"$json"
  fi
  jq -r '.checks[]? | "- \(.type): \(.status)"' <<<"$json"
  jq -r '.checks[]? | .findings[]? | "  - \(.message)"' <<<"$json"
  echo ""
}

# emit writes the action outputs and the step summary, then exits with rc.
emit() {
  local status="$1" rc="$2"
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    {
      echo "status=$status"
      echo "report=${load_json}"
      echo "drill_report=${drill_json}"
    } >>"$GITHUB_OUTPUT"
  fi
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
      echo "## kubox verification: \`$status\`"
      echo ""
      summary_report "Load test" "$load_json"
      summary_report "Drill" "$drill_json"
    } >>"$GITHUB_STEP_SUMMARY"
  fi
  echo "kubox verification: $status (exit $rc)"
  exit "$rc"
}

if [ ! -f "$SLICE/kubox.yaml" ]; then
  echo "::error::no kubox.yaml in '$SLICE'"
  emit error 64
fi

# The agent authors the load test; the action only points at the convention
# slot. No tests/load.js is a usage error, not a skipped check.
if [ -z "$SCRIPT" ]; then
  if [ -f "$SLICE/tests/load.js" ]; then
    SCRIPT="$SLICE/tests/load.js"
  else
    echo "::error::no load test at $SLICE/tests/load.js — write it first (the agent authors the k6 test; thresholds are the contract)"
    emit error 64
  fi
fi

case "$CLUSTER" in
  kind | existing) ;;
  *)
    echo "::error::unknown cluster '$CLUSTER' — use kind or existing"
    emit error 64
    ;;
esac

if [ "$KUBOX_VERSION" = "source" ]; then
  echo "--- build kubox from source ($ACTION_PATH)"
  mkdir -p "$KUBOX_INSTALL_DIR"
  (cd "$ACTION_PATH" && go build -o "$KUBOX_INSTALL_DIR/kubox" ./cmd/kubox)
  "$KUBOX_INSTALL_DIR/kubox" --version
else
  echo "--- install kubox ($KUBOX_VERSION)"
  sh "$ACTION_PATH/install.sh"
fi

if [ "$CLUSTER" = "kind" ]; then
  KIND_CREATED="${KIND_NAME:-kubox-ci}"
  if ! command -v kind >/dev/null 2>&1; then
    arch="$(uname -m)"
    case "$arch" in
      x86_64 | amd64) arch=amd64 ;;
      aarch64 | arm64) arch=arm64 ;;
    esac
    echo "--- install kind ($arch)"
    curl -fsSL -o "$KUBOX_INSTALL_DIR/kind" "https://kind.sigs.k8s.io/dl/v0.33.0/kind-linux-$arch"
    chmod +x "$KUBOX_INSTALL_DIR/kind"
  fi
  echo "--- kind create cluster ($KIND_CREATED)"
  kind create cluster --name "$KIND_CREATED" --wait 120s
fi

if [ "$DRILL" = "scale-under-load" ] && [ "$CLUSTER" = "kind" ]; then
  echo "--- metrics-server (the HPA needs it)"
  kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
  kubectl -n kube-system patch deployment metrics-server --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
  kubectl -n kube-system rollout status deployment/metrics-server --timeout=120s
fi

if [ "$BUILD" = "true" ]; then
  if grep -q '^[[:space:]]*build:' "$SLICE/kubox.yaml" 2>/dev/null; then
    echo "--- kubox build"
    kubox build "$SLICE"
  else
    echo "--- skip build (no app.build in kubox.yaml — the image must come from a registry)"
  fi
fi

echo "--- kubox up"
if ! kubox up "$SLICE" --name "$ENV_NAME" --ttl "$TTL"; then
  emit error 3
fi

rc=0
echo "--- kubox test load"
kubox test load "$ENV_NAME" --script "$SCRIPT" --slice "$SLICE" --report --json | tee "$LOAD_OUT" || rc=$?

if [ "$DRILL" != "none" ]; then
  echo "--- kubox test drill ($DRILL)"
  drill_rc=0
  case "$DRILL" in
    dependency-restart:*)
      kubox test drill "$ENV_NAME" --type dependency-restart \
        --dependency "${DRILL#dependency-restart:}" --json | tee "$DRILL_OUT" || drill_rc=$?
      ;;
    volume-survives:*)
      kubox test drill "$ENV_NAME" --type volume-survives \
        --dependency "${DRILL#volume-survives:}" --json | tee "$DRILL_OUT" || drill_rc=$?
      ;;
    scale-under-load)
      kubox test drill "$ENV_NAME" --type scale-under-load --script "$SCRIPT" \
        --slice "$SLICE" --timeout 3m --json | tee "$DRILL_OUT" || drill_rc=$?
      ;;
    *)
      echo "::error::unknown drill '$DRILL' — use none, dependency-restart:<service>, volume-survives:<service>, or scale-under-load"
      emit error 64
      ;;
  esac
  if [ "$rc" -eq 0 ]; then rc=$drill_rc; fi
fi

load_json="$(envelope "$LOAD_OUT")"
drill_json="$(envelope "$DRILL_OUT")"

if [ "$rc" -eq 0 ]; then
  emit pass 0
fi

# A report that says "error" — or no report at all (infra blew up before or
# during the run) — is an error; normalize to kubox's contract code 3 and
# keep 64 (usage) as-is.
status=fail
if [ -z "$load_json$drill_json" ] ||
  [ "$(jq -r '.status' <<<"$load_json" 2>/dev/null)" = "error" ] ||
  [ "$(jq -r '.status' <<<"$drill_json" 2>/dev/null)" = "error" ]; then
  status=error
fi
if [ "$status" = "error" ] && [ "$rc" -ne 64 ]; then
  rc=3
fi
emit "$status" "$rc"
