# syntax=docker/dockerfile:1
# Builds the web and migrate binaries (static, assets embedded) into a
# distroless image. Build from the repo root:
#   docker build -f deploy/docker/web.Dockerfile -t rentmap/web .

FROM golang:1.27-bookworm AS build
ARG TAILWIND_VERSION=v4.3.3
ARG TARGETARCH=amd64
WORKDIR /src

RUN curl -sSfL -o /usr/local/bin/tailwindcss \
      "https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION}/tailwindcss-linux-$( [ "$TARGETARCH" = "arm64" ] && echo arm64 || echo x64 )" \
 && chmod +x /usr/local/bin/tailwindcss

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go tool templ generate \
 && tailwindcss -i web/css/app.css -o web/static/css/app.css --minify \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/web ./cmd/web \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/admin ./cmd/admin \
 && mkdir -p /out/data/private

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/web /out/migrate /out/admin /app/
# Owned by nonroot (65532) so a fresh named volume inherits writable permissions.
COPY --from=build --chown=65532:65532 /out/data /data
ENV APP_ENV=production HTTP_ADDR=:8080 STORAGE_DIR=/data/private
# Private uploads (encrypted ID evidence, avatars): mount a volume here.
VOLUME ["/data/private"]
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/web"]
