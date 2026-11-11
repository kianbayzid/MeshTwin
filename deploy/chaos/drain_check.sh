#!/bin/sh
# Scenario d check: did the spool drain no faster than DRAIN_RATE_BYTES allows?
#   sh drain_check.sh BYTES FILES CAP TOOK   (exit 0 = cap applied)
# BYTES/FILES: spool size just before S3 returned; CAP: bytes/s; TOOK: seconds
# from the first drained file to an empty spool.
#
# The bound: the drainer sleeps size/CAP after each file. The clock starts after
# the first upload and stops when the last file is removed (before its sleep),
# so it covers the sleeps of FILES-1 files. Files are one FLUSH_AGE each, so
# similar in size: want = BYTES * (FILES-1)/FILES / CAP. We require 0.8 * want:
# the slack covers 1 s polling at each end and uneven file sizes. An uncapped
# drain of a few files takes about a second, so want must be >= 10 s for the
# test to tell the two apart. Only a lower bound: upload time only adds.
awk -v b="$1" -v f="$2" -v r="$3" -v t="$4" 'BEGIN {
	num = "^[0-9]+([.][0-9]+)?([eE][+]?[0-9]+)?$"
	if (b !~ num || f !~ num || r !~ num || t !~ num) { print "d: non-numeric input bytes=" b " files=" f " cap=" r " took=" t; exit 1 }
	if (b + 0 <= 0 || f + 0 < 2 || r + 0 <= 0) { print "d: need bytes > 0, files >= 2, cap > 0 (bytes=" b " files=" f " cap=" r ")"; exit 1 }
	want = b * (f - 1) / f / r
	if (want < 10) { printf "d: only %.1fs of capped drain expected; spool more (longer outage) or lower DRAIN_CAP\n", want; exit 1 }
	ok = t >= 0.8 * want
	printf "d: %d B in %d files at cap %d B/s: expected >= %.0fs (0.8 x %.0fs), measured %ds: %s\n", b, f, r, 0.8 * want, want, t, ok ? "cap applied" : "cap NOT applied"
	exit !ok
}'
