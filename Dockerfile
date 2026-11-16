# SPDX-FileCopyrightText: Outwright contributors
# SPDX-License-Identifier: MIT OR Apache-2.0
#
# Build it yourself: docker build -t outwright-imap-push .
# For another platform: docker buildx build --platform linux/arm64 -t outwright-imap-push .

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/outwright-imap-push ./cmd/outwright-imap-push \
    && mkdir -p /out/state

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/outwright-imap-push /outwright-imap-push
# Owned by the user the worker runs as, so a named volume on /state is writable.
COPY --from=build --chown=65532:65532 /out/state /state
VOLUME ["/config", "/state"]
USER nonroot:nonroot
ENTRYPOINT ["/outwright-imap-push"]
