# Two stages: uv resolves the locked dependencies into a venv in the build stage;
# the runtime stage gets only that venv on the same slim Python (no uv, no dev
# deps, no compilers). Base images pinned by tag AND digest.
FROM ghcr.io/astral-sh/uv:0.13.1@sha256:4a1f3666816dafadd20ceb71eac9a7d56d7ef30279f7d64048519ab901ada336 AS uv

FROM python:3.12.16-slim-bookworm@sha256:935c3c5b12357a7e10caa9c1e6ca5692344d97cf229832ef584be2cec9fc03df AS build
COPY --from=uv /uv /bin/uv
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never UV_PROJECT_ENVIRONMENT=/app/.venv
WORKDIR /src
# Dependencies first so code edits don't re-download them.
COPY pyproject.toml uv.lock ./
RUN uv sync --locked --no-dev --no-install-project
COPY README.md ./
COPY src ./src
RUN uv sync --locked --no-dev --no-editable
# storage.connect() runs INSTALL httpfs; doing it here bakes the extension into
# HOME so jobs never download code at run time (and HOME can stay read-only).
RUN mkdir -p /home/app && HOME=/home/app /app/.venv/bin/python -c "import duckdb; duckdb.connect().execute('INSTALL httpfs')" \
 && chmod -R a+rX /home/app

FROM python:3.12.16-slim-bookworm@sha256:935c3c5b12357a7e10caa9c1e6ca5692344d97cf229832ef584be2cec9fc03df
# The jobs never install anything: drop the base image's pip (and its CVEs).
RUN rm -rf /usr/local/bin/pip /usr/local/bin/pip3 /usr/local/bin/pip3.12 \
      /usr/local/lib/python3.12/site-packages/pip /usr/local/lib/python3.12/site-packages/pip-*.dist-info
COPY --from=build /app/.venv /app/.venv
COPY --from=build /home/app /home/app
# Numeric UID so Kubernetes runAsNonRoot can verify it. Writable paths come
# from volumes (/tmp, the DEM cache); the root filesystem can be read-only.
ENV PATH=/app/.venv/bin:$PATH HOME=/home/app TMPDIR=/tmp PYTHONUNBUFFERED=1
USER 10001:10001
# cwd on the /tmp volume: DuckDB spills to ./.tmp under memory pressure.
WORKDIR /tmp
ENTRYPOINT ["meshtwin-analytics"]
