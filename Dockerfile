# syntax=docker/dockerfile:1
# The codereview CLI image the local executor runs. Pin it to a release.
ARG BOT_IMAGE=ghcr.io/divmora/code-review-bot:v0.3.0

FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X github.com/divmora/code-review-console/internal/version.Version=${VERSION}" \
    -o /out/code-review-console ./cmd/console

FROM ${BOT_IMAGE} AS bot

FROM debian:bookworm-slim
ARG VERSION=0.0.0-dev
LABEL org.opencontainers.image.title="code-review-console" \
      org.opencontainers.image.description="Divmora Code Review Console: webhooks, queue, executors and admin UI for code-review-bot" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/divmora/code-review-console" \
      org.opencontainers.image.licenses="BUSL-1.1" \
      org.opencontainers.image.vendor="DIVMORA Technologies"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --home /home/app --create-home app
COPY --from=builder /out/code-review-console /usr/local/bin/code-review-console
# The local executor runs this CLI for each review.
COPY --from=bot /usr/local/bin/codereview /usr/local/bin/codereview
USER 10001
WORKDIR /home/app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/code-review-console"]
CMD ["serve"]
