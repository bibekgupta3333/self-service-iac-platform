# syntax=docker/dockerfile:1
# Control-plane API image: static Go binary on distroless, running as non-root.
# Base images are pinned by digest (AGENTS.md rule 4); bump tag and digest together.

FROM golang:1.26-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -o /out/iacp-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/iacp-api /iacp-api
USER nonroot:nonroot
# Inside its own network namespace the container must listen on all interfaces.
# IACP_AUTH_MODE is deliberately not set: the operator must choose it, or the API refuses to start.
ENV IACP_HTTP_ADDR=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/iacp-api"]
