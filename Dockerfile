# Puente: one static Go binary serving the API, the web app and the worker
# loop. Three stages: the web build, the Go build, a small runtime.

# --- Web app ------------------------------------------------------------------
FROM node:24-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- Go binary ----------------------------------------------------------------
FROM golang:1.27-bookworm AS go
WORKDIR /src
ENV CGO_ENABLED=0
# Dependencies first so code changes don't bust the module cache layer.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -trimpath -ldflags "-s -w" -o /out/puente ./cmd/puente

# --- Runtime: the binary and the web build, nothing else ----------------------
FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates wget && adduser -D -u 10001 puente
COPY --from=go /out/puente /usr/local/bin/puente
COPY --from=web /src/web/dist /app/web/dist
USER puente
WORKDIR /app
ENV PUENTE_STATIC_DIR=/app/web/dist \
    PORT=8000
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=3s --start-period=15s \
    CMD wget -qO- "http://127.0.0.1:${PORT}/api/health" >/dev/null || exit 1
# serve migrates on start; every new visitor sandbox is seeded.
CMD ["puente", "serve"]
