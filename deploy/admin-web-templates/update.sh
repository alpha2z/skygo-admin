#!/bin/sh
# Pull and recreate this component without touching another service.
set -eu
cd "$(dirname "$0")"
[ -f .env ] || { echo 'Create and configure .env from .env.example first.' >&2; exit 1; }
docker compose --env-file .env -f compose.yaml config --quiet
docker compose --env-file .env -f compose.yaml pull
exec ./start.sh
