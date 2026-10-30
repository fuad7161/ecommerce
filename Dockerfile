# syntax=docker/dockerfile:1

# ---- build ----
FROM golang:1.25-bookworm AS build
WORKDIR /src

ARG TARGETARCH
ARG TAILWIND_VERSION=v3.4.17
RUN case "${TARGETARCH:-amd64}" in arm64) arch=arm64 ;; *) arch=x64 ;; esac && \
    curl -fsSL -o /usr/local/bin/tailwindcss \
      "https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION}/tailwindcss-linux-${arch}" && \
    chmod +x /usr/local/bin/tailwindcss

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN tailwindcss -c tailwind.config.js -i web/styles/input.css -o web/static/css/app.css --minify
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /app/server
# Timezone data for Asia/Dhaka.
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/server"]
CMD ["serve"]
