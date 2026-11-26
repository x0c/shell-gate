FROM node:22-bookworm AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26.11-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-build /src/internal/server/web/ /src/internal/server/web/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /shell-gate ./cmd/shell-gate

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 shellgate && useradd --uid 10001 --gid shellgate --create-home shellgate \
    && mkdir -p /data && chown shellgate:shellgate /data && chmod 700 /data
COPY --from=build /shell-gate /usr/local/bin/shell-gate
USER 10001:10001
ENV SHELL_LISTEN=0.0.0.0:18765 SHELL_DATA_DIR=/data
EXPOSE 18765
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s CMD curl -fsS http://127.0.0.1:18765/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/shell-gate"]
