# Three stages: build the web console (web/) with Node, compile the API with the
# console embedded, ship only the static binary on distroless as a non-root user.
# Node and the Go toolchain never reach the final image.
# Build: docker build --build-context web=web -t meshtwin/api:<tag> api
# Base images pinned by tag AND digest.
FROM node:22.23.5-bookworm-slim@sha256:e9f565d32859f0cbfb52125eafe8be39390f24dac302dc3cdf566e8ba3ab21ee AS console
WORKDIR /web
COPY --from=web package.json package-lock.json ./
# Exactly the lockfile; no install scripts run.
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY --from=web . .
RUN npm run build

FROM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the committed placeholder with the real console before embedding it.
COPY --from=console /web/dist/ internal/server/console/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/api /api
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/api"]
