#!/bin/sh
# MeshTwin DDIL chaos demo on the local k3d cluster (TEST DATA). Run: make chaos
#
# A publisher Job replays SYNTHETIC test data (testdata/synthetic/sea-seed1-10m.ndjson)
# into the bundled mosquitto at QoS 1 while this script breaks things:
#   a. storage outage   SeaweedFS scaled to 0 for OUTAGE s: spool grows, ingester stays Ready
#   b. flapping link    SeaweedFS down/up FLAPS times, FLAP s each: spool rises and falls
#   c. ingester killed  graceful pod delete while S3 is down: the new pod finds the spool
#                       on the PVC; mosquitto queues the QoS 1 messages meanwhile
#   d. limited link     DRAIN_RATE_BYTES=DRAIN_CAP: drain time ~ spooled bytes / cap (drain_check.sh)
# then the zero-loss check: distinct reception_id in s3://meshtwin/receptions/ must
# equal the distinct reception_id of the same file through `ingester -mode replay
# -local-dir` (computed when the chaos image is built). Exits non-zero otherwise.
#
# Scope: the run starts by DELETING s3://meshtwin/receptions/ (k3d test bucket
# only), because the smoke fixture is the same synthetic SEA network on the same
# date and could share reception_ids, which would hide losses. Re-seed with make smoke.
#
# NOT covered by the design, and not staged around here:
# - SIGKILL / OOM kill / node loss: rows still in memory (up to FLUSH_AGE) are lost.
#   The broker already has our QoS 1 acks for them, so it won't resend. Only a
#   graceful stop (SIGTERM, as in c) flushes them to S3 or the spool.
# - Public broker (QoS 0, clean session): messages published while the ingester is
#   down are lost. That is the politeness trade-off, not a bug.
# - Bundled mosquitto crash: queued messages since its last autosave (60 s) are lost.
# - Spool full: the oldest files are evicted (MeshTwinSpoolEvicting fires).
set -eu

CTX=k3d-meshtwin
NS=meshtwin
REL=meshtwin
KC="kubectl --context $CTX"
K="$KC -n $NS"
HERE=$(cd "$(dirname "$0")" && pwd)
OUTAGE=${OUTAGE:-180}
FLAP=${FLAP:-20}
FLAPS=${FLAPS:-3}
DRAIN_CAP=${DRAIN_CAP:-1024}
METRICS="/api/v1/namespaces/$NS/services/$REL-ingester:http/proxy/metrics"
ORIG_DRAIN=""

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
fail() { log "FAIL: $*"; exit 1; }

# One metric value from the ingester (via the API server proxy, so it survives
# pod restarts without a port-forward); "n/a" while no pod is Ready.
metric() {
	$KC get --raw "$METRICS" 2>/dev/null | awk -v m="$1" '$1 == m { v = $2 } END { print (v == "" ? "n/a" : v) }'
}
ready() {
	$K get pod -l app.kubernetes.io/component=ingester \
		-o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}'
}
status() {
	log "$1 | storage_up=$(metric meshtwin_ingest_storage_up)" \
		"spool_bytes=$(metric meshtwin_ingest_spool_bytes)" \
		"spool_files=$(metric meshtwin_ingest_spool_files)" \
		"oldest_s=$(metric meshtwin_ingest_spool_oldest_age_seconds)" \
		"writes=$(metric meshtwin_ingest_spool_writes_total)" \
		"uploads=$(metric meshtwin_ingest_spool_uploads_total)" \
		"evicted=$(metric meshtwin_ingest_spool_evicted_files_total)" \
		"buffered=$(metric meshtwin_ingest_rows_buffered) ready=$(ready) restarts=$(restarts)"
}
# messages_total by result for the current pod (counters reset with each pod).
results() {
	log "$1 | messages_total: $($KC get --raw "$METRICS" 2>/dev/null |
		sed -n 's/^meshtwin_ingest_messages_total{result="\([^"]*\)"} \(.*\)/\1=\2/p' | tr '\n' ' ')"
}
# Firing/pending alerts from the in-cluster Prometheus.
alerts() {
	log "$1 | alerts: $($KC get --raw "/api/v1/namespaces/$NS/services/$REL-prometheus:http/proxy/api/v1/alerts" 2>/dev/null |
		grep -o '"alertname":"[^"]*"\|"state":"[^"]*"' | sed 's/.*:"\(.*\)"/\1/' | paste -sd' ' -)"
}
restarts() {
	$K get pod -l app.kubernetes.io/component=ingester -o jsonpath='{.items[*].status.containerStatuses[0].restartCount}'
}
# until_metric NAME OP VALUE TIMEOUT_S: poll every 5 s (awk does the numeric compare).
until_metric() {
	t=0
	while ! metric "$1" | awk -v op="$2" -v x="$3" '
		$1 == "n/a" { exit 1 }
		{ exit !((op == "==" && $1 == x) || (op == ">" && $1 > x)) }'; do
		[ "$t" -ge "$4" ] && return 1
		sleep 5
		t=$((t + 5))
	done
}
s3_down() {
	$K scale statefulset/$REL-seaweedfs --replicas=0 >/dev/null
	$K wait --for=delete pod -l app.kubernetes.io/component=seaweedfs --timeout=120s >/dev/null 2>&1 || true
}
s3_up() {
	$K scale statefulset/$REL-seaweedfs --replicas=1 >/dev/null
	$K rollout status statefulset/$REL-seaweedfs --timeout=180s >/dev/null
}
drained() { # $1 = label; waits for an empty spool and S3 back up
	until_metric meshtwin_ingest_storage_up == 1 180 || fail "$1: storage_up never returned to 1"
	until_metric meshtwin_ingest_spool_files == 0 600 || fail "$1: spool did not drain"
	status "$1 drained"
}
# job_wait NAME TIMEOUT_S: 0 when the Job succeeded, 1 when it failed or timed out.
job_wait() {
	t=0
	while :; do
		s=$($K get job "$1" -o jsonpath='{.status.conditions[?(@.status=="True")].type}')
		case "$s" in *Complete*) return 0 ;; *Failed*) return 1 ;; esac
		[ "$t" -ge "$2" ] && return 1
		sleep 5
		t=$((t + 5))
	done
}
s3_job() { # wipe | count
	$K delete job "chaos-$1" --ignore-not-found >/dev/null
	sed "s/CHAOS_STEP/$1/g" "$HERE/s3-job.yaml" | $K apply -f - >/dev/null
	rc=0
	job_wait "chaos-$1" 600 || rc=1
	$K logs "job/chaos-$1" --tail=20 | while IFS= read -r l; do log "  $1: $l"; done
	return $rc
}
cleanup() {
	# Never leave the cluster broken if the script stops part-way.
	$K scale statefulset/$REL-seaweedfs --replicas=1 >/dev/null 2>&1 || true
	[ -z "$ORIG_DRAIN" ] || $K set env deploy/$REL-ingester DRAIN_RATE_BYTES="$ORIG_DRAIN" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# --- Setup ---------------------------------------------------------------------
$KC get deploy/$REL-ingester -n $NS >/dev/null || fail "no $REL-ingester in $CTX/$NS: run make deploy first"
qos=$($K get deploy/$REL-ingester -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="MQTT_QOS")].value}')
[ "$qos" = 1 ] || fail "ingester is not the own-observer setting (MQTT_QOS=$qos); the demo needs the bundled mosquitto"
log "setup: FLUSH_AGE=$($K get deploy/$REL-ingester -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="FLUSH_AGE")].value}')" \
	"OUTAGE=${OUTAGE}s FLAP=${FLAP}s x$FLAPS DRAIN_CAP=${DRAIN_CAP}B/s"
s3_up
$K delete job chaos-publisher --ignore-not-found >/dev/null
s3_job wipe || fail "wipe job failed"
$K apply -f "$HERE/publisher.yaml" >/dev/null
log "setup: TEST DATA publisher started (sea-seed1-10m.ndjson, QoS 1, speed 0.5)"
sleep 30
status "setup"

# --- a. Storage outage ---------------------------------------------------------
log "a. storage outage: SeaweedFS -> 0 for ${OUTAGE}s"
s3_down
t=0
while [ "$t" -lt "$OUTAGE" ]; do
	sleep 30
	t=$((t + 30))
	status "a. outage +${t}s"
	[ "$(ready)" = True ] || fail "a: ingester not Ready during the outage"
done
[ "$(metric meshtwin_ingest_storage_up)" = 0 ] || fail "a: storage_up is not 0 with S3 down"
until_metric meshtwin_ingest_spool_files '>' 0 1 || fail "a: nothing spooled (is FLUSH_AGE shorter than OUTAGE?)"
alerts "a. end of outage"
log "a. restoring SeaweedFS"
s3_up
drained "a."

# --- b. Flapping link ----------------------------------------------------------
i=1
while [ "$i" -le "$FLAPS" ]; do
	log "b. flap $i/$FLAPS: SeaweedFS down ${FLAP}s"
	s3_down
	sleep "$FLAP"
	status "b. flap $i down"
	s3_up
	sleep "$FLAP"
	status "b. flap $i up"
	i=$((i + 1))
done
drained "b."

# --- c. Ingester killed mid-outage ---------------------------------------------
log "c. SeaweedFS down, then graceful delete of the ingester pod"
s3_down
until_metric meshtwin_ingest_spool_files '>' 0 120 || fail "c: nothing spooled before the kill"
status "c. before kill"
results "c. old pod before kill"
before=$(metric meshtwin_ingest_spool_files)
pod=$($K get pod -l app.kubernetes.io/component=ingester -o jsonpath='{.items[0].metadata.name}')
$K delete pod "$pod" --wait=true >/dev/null # SIGTERM: final flush goes to the spool
log "c. $pod deleted; mosquitto queues QoS 1 messages for the persistent session meanwhile"
t=0 # poll: right after the delete the replacement pod may not exist yet
until [ "$(ready)" = True ]; do
	[ "$t" -ge 180 ] && fail "c: new ingester pod not Ready with S3 down"
	sleep 5
	t=$((t + 5))
done
status "c. new pod Ready (S3 still down)"
after=$(metric meshtwin_ingest_spool_files)
awk -v a="$after" -v b="$before" 'BEGIN { exit !(a >= b) }' \
	|| fail "c: new pod sees $after spool files, old pod had $before"
sleep 30 # let mosquitto redeliver the queued QoS 1 backlog
results "c. new pod +30s"
log "c. new pod picked up the spool from the PVC ($before -> $after files); restoring SeaweedFS"
s3_up
drained "c."

# --- d. Limited link -----------------------------------------------------------
ORIG_DRAIN=$($K get deploy/$REL-ingester -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="DRAIN_RATE_BYTES")].value}')
log "d. limited link: DRAIN_RATE_BYTES $ORIG_DRAIN -> $DRAIN_CAP (rollout)"
$K set env deploy/$REL-ingester DRAIN_RATE_BYTES="$DRAIN_CAP" >/dev/null
$K rollout status deploy/$REL-ingester --timeout=180s >/dev/null
s3_down
sleep 90
status "d. before restore"
alerts "d. during outage"
bytes=$(metric meshtwin_ingest_spool_bytes)
files=$(metric meshtwin_ingest_spool_files)
up0=$(metric meshtwin_ingest_spool_uploads_total)
[ "$up0" != n/a ] || fail "d: no uploads_total metric before restore"
s3_up
# The clock starts at the first drained file, not at the restore: the S3 restart
# and the drainer's backoff (up to 30 s) are not drain time. 1 s polls here.
t=0
until metric meshtwin_ingest_spool_uploads_total | awk -v u0="$up0" '{ exit !($1 != "n/a" && $1 > u0) }'; do
	[ "$t" -ge 300 ] && fail "d: no spool upload within 300s of the restore"
	sleep 1
	t=$((t + 1))
done
start=$(date +%s)
until [ "$(metric meshtwin_ingest_spool_files)" = 0 ]; do
	[ $(($(date +%s) - start)) -ge 900 ] && fail "d: spool did not drain within 900s"
	sleep 1
done
took=$(($(date +%s) - start))
msg=$(sh "$HERE/drain_check.sh" "$bytes" "$files" "$DRAIN_CAP" "$took") || fail "$msg"
log "$msg"
drained "d."
$K set env deploy/$REL-ingester DRAIN_RATE_BYTES="$ORIG_DRAIN" >/dev/null
ORIG_DRAIN=""
$K rollout status deploy/$REL-ingester --timeout=180s >/dev/null
log "d. DRAIN_RATE_BYTES restored"

# --- Zero-loss check -----------------------------------------------------------
[ "$($K get job chaos-publisher -o jsonpath='{.status.succeeded}')" = 1 ] \
	&& log "warning: publisher finished before the scenarios did; lower its -speed"
log "waiting for the publisher to finish"
job_wait chaos-publisher 2400 || fail "publisher job failed"
until_metric meshtwin_ingest_rows_buffered == 0 300 || fail "rows still buffered in memory"
until_metric meshtwin_ingest_spool_files == 0 300 || fail "spool not empty at the end"
status "final"
results "final"
alerts "final"
s3_job count || fail "zero-loss check failed"
log "PASS: every reception reached S3 through all four scenarios"
