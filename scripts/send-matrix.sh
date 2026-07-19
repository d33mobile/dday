#!/usr/bin/env bash
# Send a message to a Matrix room as a bot user.
#
# Config comes from matrix.env (gitignored) in the repo root, or from the
# environment. Required: MATRIX_HOMESERVER, MATRIX_USER, MATRIX_ROOM,
# MATRIX_PASSWORD. Message text is the first argument (default "hello world").
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${MATRIX_ENV:-$ROOT/matrix.env}"

if [[ -f "$ENV_FILE" ]]; then
  # shellcheck disable=SC1090
  set -a; source "$ENV_FILE"; set +a
fi

: "${MATRIX_HOMESERVER:?set MATRIX_HOMESERVER}"
: "${MATRIX_USER:?set MATRIX_USER}"
: "${MATRIX_ROOM:?set MATRIX_ROOM}"
: "${MATRIX_PASSWORD:?set MATRIX_PASSWORD}"

MESSAGE="${1:-hello world}"

# Strip the leading @ and any :homeserver part -> localpart for m.id.user login.
localpart="${MATRIX_USER#@}"
localpart="${localpart%%:*}"

echo "Logging in as $MATRIX_USER on $MATRIX_HOMESERVER ..." >&2
token="$(
  curl -sf -X POST "$MATRIX_HOMESERVER/_matrix/client/v3/login" \
    -H 'Content-Type: application/json' \
    --data "$(jq -n --arg u "$localpart" --arg p "$MATRIX_PASSWORD" \
      '{type:"m.login.password",identifier:{type:"m.id.user",user:$u},password:$p,initial_device_display_name:"ddaybot-cli"}')" \
    | jq -r '.access_token'
)"

if [[ -z "$token" || "$token" == "null" ]]; then
  echo "login failed" >&2
  exit 1
fi

# URL-encode the room id (! and : are reserved).
room_enc="$(jq -rn --arg r "$MATRIX_ROOM" '$r|@uri')"
# Transaction id: unique-ish without relying on epoch tooling.
txn="ddaybot-$RANDOM$RANDOM"

echo "Sending message to $MATRIX_ROOM ..." >&2
event_id="$(
  curl -sf -X PUT \
    "$MATRIX_HOMESERVER/_matrix/client/v3/rooms/$room_enc/send/m.room.message/$txn" \
    -H "Authorization: Bearer $token" \
    -H 'Content-Type: application/json' \
    --data "$(jq -n --arg b "$MESSAGE" '{msgtype:"m.text",body:$b}')" \
    | jq -r '.event_id'
)"

if [[ -z "$event_id" || "$event_id" == "null" ]]; then
  echo "send failed" >&2
  exit 1
fi

echo "sent: $event_id" >&2
