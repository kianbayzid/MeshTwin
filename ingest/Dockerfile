# Multi-stage build: compile in the full Go image, ship only the static binary
# on distroless (no shell, no package manager), running as a non-root user.
# Phase 6 pins these by digest.
FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ingester ./cmd/ingester

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ingester /ingester
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/ingester"]
