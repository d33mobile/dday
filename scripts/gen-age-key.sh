#!/usr/bin/env bash
# Generate the ed25519 keypair used to encrypt registration-link tokens.
# Keys land in config/ and are gitignored — never commit them.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KEY="${AGE_KEY:-$ROOT/config/dday_ed25519}"

mkdir -p "$(dirname "$KEY")"

if [[ -f "$KEY" ]]; then
  echo "key already exists: $KEY (refusing to overwrite)" >&2
  exit 0
fi

ssh-keygen -t ed25519 -N "" -C "dday-register" -f "$KEY"
echo "generated:"
echo "  private: $KEY"
echo "  public:  $KEY.pub"
