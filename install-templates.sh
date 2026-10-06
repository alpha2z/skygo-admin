#!/bin/sh
# Download public API templates without changing an existing deployment.
set -eu
umask 077

usage() {
  cat <<'HELP'
Usage: install-templates.sh [--output DIRECTORY] [--ref main|COMMIT_SHA]

Download Admin API configuration examples into a NEW directory.
  --output DIRECTORY  Destination (default: ./admin-api-templates)
  --ref REF           main or a full 40-character commit SHA (default: main)
  -h, --help          Show this help

Requires curl and tar. Existing destinations are never overwritten.
Does not generate secrets, install Docker, migrate databases or start services.
HELP
}
output=./admin-api-templates
ref=main
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output|--ref)
      option=$1
      [ "$#" -ge 2 ] && [ -n "$2" ] || { echo 'Missing option value.' >&2; exit 2; }
      case "$option" in --output) output=$2;; --ref) ref=$2;; esac
      shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo 'Unknown option. Use --help.' >&2; exit 2 ;;
  esac
done
case "$ref" in
  main) ;;
  *) [ "${#ref}" -eq 40 ] || { echo 'Use main or a full commit SHA.' >&2; exit 2; }
     case "$ref" in *[!0-9a-f]*) echo 'Invalid commit SHA.' >&2; exit 2;; esac ;;
esac
case "$output" in /*|./*|../*) ;; *) output=./$output;; esac
if [ -e "$output" ] || [ -L "$output" ]; then
  echo 'Destination already exists; nothing was overwritten. Choose a new --output directory.' >&2
  exit 1
fi
for tool in curl tar mktemp; do
  command -v "$tool" >/dev/null 2>&1 || { echo "Required command unavailable: $tool" >&2; exit 1; }
done
stage=$(mktemp -d "${TMPDIR:-/tmp}/skygo-admin-templates.XXXXXXXX")
trap 'rm -rf "$stage"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
curl --proto '=https' --tlsv1.2 --fail --location --retry 3 \
  --connect-timeout 15 --max-time 180 \
  --output "$stage/source.tar.gz" \
  "https://codeload.github.com/alpha2z/skygo-admin/tar.gz/$ref"
prefix="skygo-admin-$ref/deploy/admin-api-templates"
# Extract only the documented template files, not the entire source tree.
for file in README.md admin.env.example generate-keys.py \
  private/management-dsn.example private/jwt.example private/bootstrap.example \
  private/signing.example private/signing.pub.example private/build-public.example \
  private/github.json.example private/github-read-token.example private/smtp-password.example; do
  tar -xzf "$stage/source.tar.gz" -C "$stage" "$prefix/$file"
  [ -f "$stage/$prefix/$file" ] && [ ! -L "$stage/$prefix/$file" ] || {
    echo 'Incomplete or invalid template archive.' >&2; exit 1;
  }
done
# mkdir is the final exclusive destination claim, including concurrent installs.
mkdir "$output" || { echo 'Could not create a new destination directory.' >&2; exit 1; }
cp -R "$stage/$prefix/." "$output/"
printf 'Installed templates in %s (source: %s).\n' "$output" "$ref"
printf '%s\n' 'Read README.md before configuring the service. Existing private files were not changed.'
