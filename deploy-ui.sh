#!/usr/bin/env sh
# Puts a new build of the UI live without restarting Nightjar. Nightjar serves the UI from
# FRONTEND_DIR on every request, so point it here once (FRONTEND_DIR=<this folder>/web/current, then
# one restart), and after that this script is all a UI change needs. Go changes still need a rebuild
# and a restart.
#
# Each build goes into web/releases/<time>, and web/current is switched to it in one step, so no
# request ever sees half a build. Tabs opened on an earlier build still load its pages: the last
# week's page files are carried over into the new build.
set -eu
cd "$(dirname "$0")"
ROOT=$(pwd)
WEB="$ROOT/web"
REL="$WEB/releases/$(date +%Y%m%d-%H%M%S)"

need() { command -v "$1" >/dev/null 2>&1 || { echo "error: $1 is not installed ($2)" >&2; exit 1; }; }
need npm "https://nodejs.org/"

mkdir -p "$WEB/releases"
echo "==> Building the UI into $REL"
(cd frontend && { [ -d node_modules ] || npm ci --no-audit --no-fund; } && npx tsc -b && npx vite build --outDir "$REL" --emptyOutDir --logLevel warn)

if [ -d "$WEB/current/assets" ]; then
  find "$WEB/current/assets" -type f -mtime -7 -exec sh -c 'for f; do [ -e "$0/${f##*/}" ] || cp -p "$f" "$0/"; done' "$REL/assets" {} +
fi

ln -sfn "$REL" "$WEB/current.next"
mv -T "$WEB/current.next" "$WEB/current"
echo "==> Live: $WEB/current -> $REL"

# Keep the five newest builds.
ls -1d "$WEB"/releases/* | head -n -5 | xargs -r rm -rf

if ! grep -qs "^FRONTEND_DIR=$WEB/current\$" "$ROOT/.env"; then
  echo "    Nightjar serves this only with FRONTEND_DIR=$WEB/current in .env (then restart it once)."
fi
