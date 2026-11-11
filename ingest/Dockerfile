# Multi-stage build: compile in the full Go image, ship only the static binary
# on distroless (no shell, no package manager), running as a non-root user.
# Base images pinned by tag AND digest: the tag says what it is, the digest
# makes the build reproducible even if the tag is re-pushed.
FROM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ingester ./cmd/ingester
RUN mkdir -p /out/spool

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/ingester /ingester
# Empty, nonroot-owned: a new Docker named volume mounted here copies the ownership.
COPY --from=build --chown=65532:65532 /out/spool /spool
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/ingester"]
