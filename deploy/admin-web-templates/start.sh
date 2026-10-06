#!/bin/sh
# Start this component only, using an already available image.
set -eu
cd "$(dirname "$0")"
[ -f .env ] || { echo 'Create and configure .env from .env.example first.' >&2; exit 1; }
docker compose --env-file .env -f compose.yaml config --quiet
exec docker compose --env-file .env -f compose.yaml up -d --no-deps --pull never
