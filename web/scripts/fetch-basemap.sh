#!/bin/sh
# Extract a basemap from the Protomaps daily planet build (OpenStreetMap data, ODbL).
# One-time online step; afterwards the app serves public/basemap/<name>.pmtiles itself and works offline.
# Usage: scripts/fetch-basemap.sh [region] [west,south,east,north]   region detail, max zoom 14 (default: sea)
#        scripts/fetch-basemap.sh us                                  US overview (scripts/us-region.json), max zoom 9
set -eu
REGION=${1:-sea}
BBOX=${2:--122.6,47.3,-121.8,47.9}  # same box as the terrain tiles (terrain.json bounds)
PMTILES=github.com/protomaps/go-pmtiles@v1.31.2
# Daily builds are kept for about a week, so default to the newest one listed.
BUILD=${BUILD:-$(curl -fsS https://build-metadata.protomaps.dev/builds.json | grep -o '"key":"[0-9]*\.pmtiles"' | tail -1 | cut -d'"' -f4)}
cd "$(dirname "$0")/.."
mkdir -p public/basemap
if [ "$REGION" = us ]; then
  # Contiguous US, Alaska and Hawaii. z9 keeps it at ~180 MB (z10 ~385 MB); region archives add street detail on top.
  set -- --region=scripts/us-region.json --maxzoom=9
else
  set -- --bbox="$BBOX" --maxzoom=14
fi
go run "$PMTILES" extract "https://build.protomaps.com/$BUILD" "public/basemap/$REGION.pmtiles" "$@"
echo "basemap: public/basemap/$REGION.pmtiles from $BUILD"
