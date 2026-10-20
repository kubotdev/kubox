#!/usr/bin/env sh
# Install kubox from GitHub Releases. Usage:
#   curl -fsSL https://raw.githubusercontent.com/kubotdev/kubox/main/install.sh | sh
#
# Env: KUBOX_VERSION (default: latest release tag), KUBOX_INSTALL_DIR
#      (default: /usr/local/bin), KUBOX_REQUIRE_SIGNATURE=1 (hard-fail
#      unless the cosign signature verifies; needs cosign on PATH).
# Verifies the sha256 checksum always, and the cosign signature when cosign
# is available (keyless, GitHub Actions OIDC).
set -eu

# Git Bash rewrites slash-containing args (URLs, regexps) into Windows paths
# unless told not to — which silently breaks the cosign identity match below.
export MSYS_NO_PATHCONV=1

REPO="kubotdev/kubox"
VERSION="${KUBOX_VERSION:-latest}"
INSTALL_DIR="${KUBOX_INSTALL_DIR:-/usr/local/bin}"

os() {
  case "$(uname -s)" in
    Linux)  echo linux ;;
    Darwin) echo darwin ;;
    MINGW*|MSYS*|CYGWIN*) echo windows ;;
    *) echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
  esac
}

arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    arm64|aarch64) echo arm64 ;;
    *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
  esac
}

sha256check() {
  # sha256sum (coreutils/Linux, Git Bash) or shasum (macOS default).
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c -
  else
    shasum -a 256 -c -
  fi
}

resolve_tag() {
  if [ "$VERSION" != "latest" ]; then echo "$VERSION"; return; fi
  # Follow the /latest redirect to the newest tag without needing the API.
  curl -fsSL -o /dev/null -w "%{url_effective}\n" "https://github.com/$REPO/releases/latest" | sed 's#.*/tag/##'
}

# verify_signature checks the cosign bundle when cosign exists. With
# KUBOX_REQUIRE_SIGNATURE=1 a missing cosign, missing bundle, or failed
# verification is fatal; otherwise the install proceeds checksum-verified.
verify_signature() {
  tmp="$1"; tag="$2"
  if ! command -v cosign >/dev/null 2>&1; then
    if [ "${KUBOX_REQUIRE_SIGNATURE:-0}" = "1" ]; then
      echo "KUBOX_REQUIRE_SIGNATURE=1 but cosign is not on PATH" >&2; exit 1
    fi
    echo "cosign not found — checksum verified, signature skipped (brew install cosign for full verification)"
    return 0
  fi
  if ! curl -fsSL -o "$tmp/checksums.txt.cosign.bundle" \
      "https://github.com/$REPO/releases/download/${tag}/checksums.txt.cosign.bundle"; then
    if [ "${KUBOX_REQUIRE_SIGNATURE:-0}" = "1" ]; then
      echo "KUBOX_REQUIRE_SIGNATURE=1 but no signature bundle published" >&2; exit 1
    fi
    echo "no signature bundle published — checksum verified only"
    return 0
  fi
  (cd "$tmp" && cosign verify-blob --bundle checksums.txt.cosign.bundle \
    --certificate-identity-regexp "^https://github.com/$REPO/\.github/workflows/release\.yml@" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt) || {
    echo "signature verification FAILED — aborted" >&2; exit 1
  }
  echo "signature verified"
}

main() {
  need() { command -v "$1" >/dev/null 2>&1 || { echo "need $1" >&2; exit 1; }; }
  need curl
  if [ "$(os)" = "windows" ]; then need unzip; else need tar; fi

  tag="$(resolve_tag)"
  # Release assets strip the leading v (kubox_0.1.0_..., not kubox_v0.1.0_...).
  ver="${tag#v}"
  plat="$(os)_$(arch)"
  if [ "$(os)" = "windows" ]; then
    arc="zip"; bin_name="kubox.exe"; dest_name="kubox.exe"
  else
    arc="tar.gz"; bin_name="kubox"; dest_name="kubox"
  fi
  base="kubox_${ver}_${plat}"
  url="https://github.com/$REPO/releases/download/${tag}/${base}.${arc}"
  tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT INT TERM

  echo "downloading kubox $tag ($plat)…"
  curl -fsSL -o "$tmp/${base}.${arc}" "$url"
  curl -fsSL -o "$tmp/checksums.txt" "https://github.com/$REPO/releases/download/${tag}/checksums.txt"

  (cd "$tmp" && grep "  ${base}.${arc}\$" checksums.txt | sha256check) || {
    echo "checksum mismatch — aborted" >&2; exit 1
  }
  verify_signature "$tmp" "$tag"

  if [ "$arc" = "zip" ]; then
    (cd "$tmp" && unzip -o -q "${base}.zip")
  else
    tar -xzf "$tmp/${base}.tar.gz" -C "$tmp"
  fi
  [ -f "$tmp/$bin_name" ] || { echo "archive missing $bin_name" >&2; exit 1; }

  mkdir -p "$INSTALL_DIR"
  install -m 0755 "$tmp/$bin_name" "$INSTALL_DIR/$dest_name"
  echo "installed to $INSTALL_DIR/$dest_name"
  "$INSTALL_DIR/$dest_name" --version
}

main
