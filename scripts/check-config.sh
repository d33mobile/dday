#!/usr/bin/env bash
# Validate bot configuration before running. Extracted from the Makefile so the
# checks live in one place and can run standalone.
#
# Usage: scripts/check-config.sh [--keys]
#   --keys   also require the age SSH keypair (AGE_PUB / AGE_KEY) to exist.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${MATRIX_ENV:-$ROOT/matrix.env}"

fail() { echo "config error: $*" >&2; exit 1; }

[[ -f "$ENV_FILE" ]] || fail "'$ENV_FILE' not found. Copy matrix.env.example -> matrix.env and fill it in."

# shellcheck disable=SC1090
set -a; source "$ENV_FILE"; set +a

for var in MATRIX_HOMESERVER MATRIX_USER MATRIX_PASSWORD; do
  [[ -n "${!var:-}" ]] || fail "$var is not set in $ENV_FILE"
done

if [[ "${1:-}" == "--keys" ]]; then
  [[ -n "${AGE_PUB:-}" ]] || fail "AGE_PUB is not set in $ENV_FILE"
  [[ -n "${AGE_KEY:-}" ]] || fail "AGE_KEY is not set in $ENV_FILE"
  # Resolve relative to repo root if not absolute.
  pub="$AGE_PUB"; [[ "$pub" = /* ]] || pub="$ROOT/$pub"
  key="$AGE_KEY"; [[ "$key" = /* ]] || key="$ROOT/$key"
  [[ -f "$pub" ]] || fail "age public key '$pub' not found. Run: make keys"
  [[ -f "$key" ]] || fail "age private key '$key' not found. Run: make keys"
fi

echo "config OK ($ENV_FILE)"
