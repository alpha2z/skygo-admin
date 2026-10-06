#!/bin/sh
# Start this component only, using an already available image.
set -eu
cd "$(dirname "$0")"
[ -f .env ] || { echo 'Create and configure .env from .env.example first.' >&2; exit 1; }
for file in private/agent.json private/agent-token private/signing.pub; do
  [ -s "$file" ] || { echo "Required Agent configuration is missing or empty: $file" >&2; exit 1; }
done
if [ ! -d state ]; then
  (umask 077; mkdir state)
fi
docker compose --env-file .env -f compose.yaml config --quiet
exec docker compose --env-file .env -f compose.yaml up -d --no-deps --pull never
