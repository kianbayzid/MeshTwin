# TEST DATA ONLY. The analytics image plus the repo's SYNTHETIC fixture
# (testdata/parquet/sea-seed1-2h-hash1: CI/test data, no real packets), used by
# the k3d smoke test's seed Job (values-smoke.yaml). Never deployed otherwise.
# Build context: testdata/parquet (see the Makefile `images` target).
ARG ANALYTICS_IMAGE=meshtwin/analytics:0.7.0
FROM ${ANALYTICS_IMAGE}
LABEL org.opencontainers.image.description="MeshTwin smoke-test fixtures: SYNTHETIC test data"
COPY sea-seed1-2h-hash1/receptions /fixtures/receptions
COPY sea-seed1-2h-hash1/adverts /fixtures/adverts
