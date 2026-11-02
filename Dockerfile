# Multi-stage build: Node builds the UI, Go embeds it, the result is one static binary.
FROM node:24.22.2-alpine AS ui
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN mkdir -p ../backend/internal/web && npm run build

FROM golang:1.27.3-alpine AS go
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=ui /src/backend/internal/web/dist ./internal/web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /servermonitor ./cmd/servermonitor

FROM alpine:3.24
# iputils for ping checks; ca-certificates for HTTPS checks and Discord.
RUN apk add --no-cache ca-certificates iputils tzdata
COPY --from=go /servermonitor /usr/local/bin/servermonitor
WORKDIR /data
ENV WEB_HOST=0.0.0.0 LOG_FILE=/data/server_monitor.log HISTORY_DB=/data/history.db SERVICES_FILE=/data/services.json
EXPOSE 8080
# Host metrics need --pid=host and the host's /proc and /sys; see README.
ENTRYPOINT ["/usr/local/bin/servermonitor"]
