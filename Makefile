# MeshTwin local Kubernetes (k3d + Helm). Recipes are POSIX sh: Linux, WSL or
# Git Bash (GNU make, or mingw32-make on Windows).
#
#   make cluster images smoke   # end to end on TEST DATA (synthetic fixture)
#   make deploy                 # real settings (values.yaml), no test data
#   make chaos                  # DDIL chaos demo + zero-loss check (TEST DATA, ~25 min)
#   make web e2e                # web console checks + browser smoke tests (as CI)
#   make clean
#
# kubectl/helm always name the context explicitly; nothing uses the current one.

CLUSTER := meshtwin
CTX     := k3d-$(CLUSTER)
NS      := meshtwin
TAG     := 0.7.0# must match images.* in deploy/helm/meshtwin/values.yaml
CHART   := deploy/helm/meshtwin
KUBECTL := kubectl --context $(CTX) -n $(NS)
HELM    := helm --kube-context $(CTX) -n $(NS)
API_URL := http://127.0.0.1:8090
JOBS    := topology propagation placement terrain-tiles
# The dashboard and alert rules live in dashboards/ and are passed in as values.
CHART_FILES := --set-file grafana.dashboard=dashboards/meshtwin.json \
               --set-file prometheus.rules=dashboards/alerts.yml
VALUES  :=

.PHONY: cluster images deploy smoke chaos lint clean web e2e

cluster:
	k3d cluster create --config deploy/k3d/cluster.yaml --wait

images:
	docker build -t meshtwin/ingester:$(TAG) ingest
	# The API image builds the web console from web/ and embeds it (api/Dockerfile).
	docker build --build-context web=web -t meshtwin/api:$(TAG) api
	docker build -t meshtwin/analytics:$(TAG) analytics
	# TEST DATA image (synthetic fixture), used only by values-smoke.yaml
	docker build -t meshtwin/fixtures:smoke --build-arg ANALYTICS_IMAGE=meshtwin/analytics:$(TAG) \
		-f deploy/smoke/Dockerfile testdata/parquet
	k3d image import -c $(CLUSTER) meshtwin/ingester:$(TAG) meshtwin/api:$(TAG) \
		meshtwin/analytics:$(TAG) meshtwin/fixtures:smoke

deploy:
	$(HELM) upgrade --install meshtwin $(CHART) --create-namespace --wait --timeout 10m \
		$(CHART_FILES) $(VALUES)

# Deploy with TEST DATA, run each CronJob once (in order: the DEM jobs share
# the DEM cache), then check the API lists the region and serves its terrain.
# No bbox override: the DEM box comes from the fixture's located nodes (--root).
smoke:
	$(MAKE) deploy VALUES="-f $(CHART)/values-smoke.yaml"
	set -e; for j in $(JOBS); do \
		$(KUBECTL) delete job smoke-$$j --ignore-not-found; \
		$(KUBECTL) create job smoke-$$j --from=cronjob/meshtwin-$$j; \
		$(KUBECTL) wait --for=condition=complete --timeout=20m job/smoke-$$j \
			|| { $(KUBECTL) logs job/smoke-$$j --all-containers --tail=50; exit 1; }; \
	done
	$(KUBECTL) get jobs -o custom-columns=JOB:.metadata.name,START:.status.startTime,DONE:.status.completionTime
	sleep 31 # the API caches summaries for 30 s
	curl -fsS $(API_URL)/api/v1/regions | grep -q '"SEA"' && echo "smoke OK: $(API_URL)/api/v1/regions lists SEA"
	curl -fsS $(API_URL)/api/v1/regions/SEA/terrain.json > /dev/null && echo "terrain OK"
	curl -fsS $(API_URL)/ | grep -q '/assets/' && echo "console OK: $(API_URL)/"

# Web console checks, as the CI web job: lockfile install, self-checks, strict build.
# (The API image builds web/ itself; this target is for checking it on its own.)
web:
	cd web && npm ci && npm test && npm run build

# Browser smoke tests, as the CI e2e job: the API binary serves the e2e build and
# the API's test fixtures (no Prometheus, no basemap). Runs Playwright on this
# machine: browsers from `npx playwright install chromium`, or PW_CHANNEL=chrome
# to reuse an installed Chrome. Needs 127.0.0.1:8080 free (or E2E_LISTEN=...).
# On Windows stop `npm run dev` first: npm ci can't replace a running esbuild.exe.
E2E_LISTEN := 127.0.0.1:8080
e2e:
	cd web && npm ci && npm run build:e2e
	set -e; tmp="$$(mktemp -d)"; bin="$$tmp/api$$(cd api && go env GOEXE)"; \
	(cd api && go build -o "$$bin" ./cmd/api); \
	"$$bin" -static-dir web/dist-e2e -summaries-dir api/testdata -listen $(E2E_LISTEN) & pid=$$!; \
	trap 'kill $$pid 2>/dev/null; wait $$pid 2>/dev/null; rm -f "$$bin"; rmdir "$$tmp"' EXIT; \
	i=0; until curl -fsS -o /dev/null http://$(E2E_LISTEN)/healthz; do \
		i=$$((i+1)); kill -0 $$pid && [ $$i -lt 30 ] || { echo "API did not start"; exit 1; }; sleep 1; \
	done; \
	kill -0 $$pid || { echo "API exited: is $(E2E_LISTEN) already in use?"; exit 1; }; \
	cd web && E2E_BASE_URL=http://$(E2E_LISTEN) npm run e2e

# DDIL chaos demo (deploy/chaos/chaos.sh). TEST DATA: builds the test-only chaos
# image (mqttreplay + synthetic capture + expected ids), redeploys with a short
# FLUSH_AGE so outages spool within seconds, wipes s3://meshtwin/receptions/,
# breaks S3/ingester while publishing, and fails unless no reception is lost.
# `make deploy` afterwards restores flushAge.
chaos:
	docker build -t meshtwin/chaos:test --build-arg ANALYTICS_IMAGE=meshtwin/analytics:$(TAG) \
		--build-context testdata=testdata/synthetic -f deploy/chaos/Dockerfile ingest
	k3d image import -c $(CLUSTER) meshtwin/chaos:test
	$(MAKE) deploy VALUES="--set ingester.flushAge=15s"
	sh deploy/chaos/chaos.sh

lint:
	$(HELM) lint $(CHART) $(CHART_FILES)
	$(HELM) lint $(CHART) $(CHART_FILES) -f $(CHART)/values-smoke.yaml
	$(HELM) template meshtwin $(CHART) $(CHART_FILES) > /dev/null
	$(HELM) template meshtwin $(CHART) $(CHART_FILES) -f $(CHART)/values-smoke.yaml > /dev/null
	@echo "lint + template OK"

clean:
	k3d cluster delete $(CLUSTER)
