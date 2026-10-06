#!/bin/sh
# Install admin-web templates only; retain existing configuration and credentials.
set -eu
umask 077
output=./admin-web-templates
ref=main
while [ "$#" -gt 0 ]; do
  case "$1" in
    -h|--help)
      echo 'Usage: install-admin-web.sh [--output DIRECTORY] [--ref main|COMMIT_SHA]'
      echo 'Downloads admin-web templates only. Existing directories are never overwritten.'
      echo 'Default output: ./admin-web-templates. Does not start services.'
      exit 0 ;;
    --output|--ref)
      [ "$#" -ge 2 ] && [ -n "$2" ] || { echo 'Missing option value.' >&2; exit 2; }
      case "$1" in --output) output=$2;; --ref) ref=$2;; esac
      shift 2 ;;
    *) echo 'Unknown option. Use --help.' >&2; exit 2 ;;
  esac
done
case "$ref" in
  main) ;;
  *) [ "${#ref}" -eq 40 ] || { echo 'Use main or a full commit SHA.' >&2; exit 2; }
     case "$ref" in *[!0-9a-f]*) echo 'Invalid commit SHA.' >&2; exit 2;; esac ;;
esac
if [ -e "$output" ] || [ -L "$output" ]; then
  echo 'Destination already exists; choose a new --output directory.' >&2
  exit 1
fi
command -v curl >/dev/null 2>&1 || { echo 'curl is required.' >&2; exit 1; }
stage=$(mktemp -d "${TMPDIR:-/tmp}/skygo-admin-web-installer.XXXXXXXX")
trap 'rm -rf "$stage"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
curl --proto '=https' --tlsv1.2 --fail --location --retry 3 \
  --connect-timeout 15 --max-time 180 \
  --output "$stage/install-templates.sh" \
  "https://raw.githubusercontent.com/alpha2z/skygo-admin/$ref/install-templates.sh"
sh "$stage/install-templates.sh" --component admin-web --output "$output" --ref "$ref"
