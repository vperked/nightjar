# Multi-stage build: Node builds the UI, Go embeds it, the result is one static binary.
FROM node:24.22.2-alpine AS ui
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN mkdir -p ../backend/internal/web && npm run build

FROM golang:1.27.4-alpine AS go
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=ui /src/backend/internal/web/dist ./internal/web/dist
ARG VERSION=dev
# The agent goes inside the monitor, for the dashboard's Add server to hand out.
RUN for arch in amd64 arm64; do \
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /tmp/agent ./cmd/nightjar-agent && \
      gzip -9 -n -c /tmp/agent > internal/agentbin/dist/nightjar-agent-linux-$arch.gz; \
    done
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /nightjar ./cmd/nightjar

FROM alpine:3.24
# iputils for ping checks; ca-certificates for HTTPS checks and Discord.
RUN apk add --no-cache ca-certificates iputils tzdata
COPY --from=go /nightjar /usr/local/bin/nightjar
WORKDIR /data
ENV WEB_HOST=0.0.0.0 LOG_FILE=/data/nightjar.log HISTORY_DB=/data/history.db SERVICES_FILE=/data/services.json
EXPOSE 8080
# Host metrics need --pid=host and the host's /proc and /sys; see README.
ENTRYPOINT ["/usr/local/bin/nightjar"]
