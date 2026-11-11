#!/bin/sh
# Unit test for drain_check.sh (no cluster needed): sh deploy/chaos/drain_check_test.sh
HERE=$(cd "$(dirname "$0")" && pwd)
rc=0
# expect WANT_EXIT BYTES FILES CAP TOOK
expect() {
	want=$1
	shift
	sh "$HERE/drain_check.sh" "$@" >/dev/null
	got=$?
	[ "$got" = "$want" ] && echo "ok   exit=$got  $*" || { echo "FAIL exit=$got want=$want  $*"; rc=1; }
}
expect 0 61440 6 1024 50  # 60 KiB, 6 files: want 50 s, measured 50 s
expect 0 61440 6 1024 40  # exactly 0.8 x want
expect 0 61440 6 1024 300 # slow link: only a lower bound
expect 1 61440 6 1024 39  # just under 0.8 x want
expect 1 61440 6 1024 1   # cap missing: drained in a second
expect 1 61440 6 1024 0
expect 1 n/a 6 1024 50    # no ingester Ready when sampled
expect 1 61440 n/a 1024 50
expect 1 0 0 1024 50      # nothing spooled
expect 1 61440 1 1024 50  # one file: the clock sees no sleep
expect 1 4096 4 1024 50   # want 3 s: too little to tell capped from uncapped
expect 1 61440 6 0 50     # no cap
expect 1 -5 6 1024 50
expect 0 6.144e+04 6 1024 45 # Prometheus may print floats in exponent form
[ "$rc" = 0 ] && echo "drain_check: all cases pass"
exit "$rc"
