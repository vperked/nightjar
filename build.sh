#!/usr/bin/env sh
# Builds bin/nightjar (UI embedded) without needing make. Requires Go 1.26+ and Node 20.19+.
set -eu
cd "$(dirname "$0")"

need() { command -v "$1" >/dev/null 2>&1 || { echo "error: $1 is not installed ($2)" >&2; exit 1; }; }
need go "https://go.dev/dl/"
need npm "https://nodejs.org/"

VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)

echo "==> Building frontend"
(cd frontend && { [ -d node_modules ] || npm ci --no-audit --no-fund; } && npm run build)

echo "==> Building the agent, which goes inside the monitor"
for arch in amd64 arm64; do
  (cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o ../bin/nightjar-agent-linux-$arch ./cmd/nightjar-agent)
  gzip -9 -n -c bin/nightjar-agent-linux-$arch > backend/internal/agentbin/dist/nightjar-agent-linux-$arch.gz
done

echo "==> Building backend ($VERSION)"
(cd backend && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o ../bin/nightjar ./cmd/nightjar)

echo "==> Done: bin/nightjar"
echo "    cp env.recommended .env   # then edit it"
echo "    ./bin/nightjar"
