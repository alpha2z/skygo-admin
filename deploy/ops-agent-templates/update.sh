#!/bin/sh
# Recreate from an already imported image; managed updates use Admin.
set -eu
cd "$(dirname "$0")"
[ -f .env ] || { echo 'Create and configure .env from .env.example first.' >&2; exit 1; }
docker compose --env-file .env -f compose.yaml config --quiet
exec ./start.sh
