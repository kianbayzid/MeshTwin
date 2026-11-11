# TEST DATA ONLY. The analytics image plus the repo's SYNTHETIC fixture
# (testdata/parquet/sea-seed1-2h-hash1: CI/test data, no real packets), used by
# the k3d smoke test's seed Job (values-smoke.yaml). Never deployed otherwise.
# Build context: testdata/parquet (see the Makefile `images` target).
ARG ANALYTICS_IMAGE=meshtwin/analytics:0.7.0
FROM ${ANALYTICS_IMAGE}
LABEL org.opencontainers.image.description="MeshTwin smoke-test fixtures: SYNTHETIC test data"
# Same user as the analytics base; explicit so Trivy (DS-0002) sees it through the ARG base.
USER 10001:10001
COPY sea-seed1-2h-hash1/receptions /fixtures/receptions
COPY sea-seed1-2h-hash1/adverts /fixtures/adverts
