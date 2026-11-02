# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/social-download ./cmd/server

FROM python:3.13-alpine
ARG VERSION=dev
LABEL org.opencontainers.image.title="Social Download" \
      org.opencontainers.image.description="Download social videos and compress them to a target size" \
      org.opencontainers.image.version="${VERSION}"

RUN apk add --no-cache ca-certificates deno ffmpeg \
    && pip install --no-cache-dir --upgrade "yt-dlp[default]" \
    && addgroup -S app \
    && adduser -S -G app -h /app app \
    && mkdir -p /data \
    && chown -R app:app /data /app

COPY --from=build /out/social-download /usr/local/bin/social-download

ENV ADDR=:8080 \
    DATA_DIR=/data \
    HOME=/app \
    XDG_CACHE_HOME=/app/.cache

USER app
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q --header="X-Download-Password: $DOWNLOAD_PASSWORD" -O /dev/null http://127.0.0.1:8080/api/health || exit 1

ENTRYPOINT ["/usr/local/bin/social-download"]
