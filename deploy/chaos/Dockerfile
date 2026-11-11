# TEST DATA ONLY. Image for the chaos demo (make chaos), never deployed otherwise:
# - mqttreplay + testdata/synthetic/sea-seed1-10m.ndjson (SYNTHETIC, no real packets)
#   for the publisher Job;
# - /chaos/expected: the same file through the ingester's replay mode into a local
#   dir, built here, so the zero-loss check has its expected reception_ids;
# - the analytics image underneath for DuckDB/pyarrow (wipe + count Jobs).
# Build (see the Makefile `chaos` target):
#   docker build -f deploy/chaos/Dockerfile --build-context testdata=testdata/synthetic ingest
ARG ANALYTICS_IMAGE=meshtwin/analytics:0.7.0

FROM golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mqttreplay ./cmd/mqttreplay \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ingester ./cmd/ingester
COPY --from=testdata sea-seed1-10m.ndjson /chaos/
RUN /out/ingester -mode replay -replay-file /chaos/sea-seed1-10m.ndjson \
      -local-dir /chaos/expected -flush-rows 1000000 -http-addr 127.0.0.1:0 \
 && chmod -R a+rX /chaos # the ingester writes 0600; the runtime user (10001) must read it

FROM ${ANALYTICS_IMAGE}
LABEL org.opencontainers.image.description="MeshTwin chaos demo: SYNTHETIC test data"
# Same user as the analytics base; explicit so Trivy (DS-0002) sees it through the ARG base.
USER 10001:10001
COPY --from=build /out/mqttreplay /usr/local/bin/mqttreplay
COPY --from=build /chaos/sea-seed1-10m.ndjson /chaos/
COPY --from=build /chaos/expected/receptions /chaos/expected/receptions
