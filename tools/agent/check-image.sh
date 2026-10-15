#!/usr/bin/env bash
# Checks an agent image: it runs as an arbitrary uid on a read-only filesystem,
# and its omp is the version the manifest pins, as model.yml checks on the
# runner. Run from the repo root: check-image.sh <image-ref>
set -eu
ref="$1"
# The caller picks the uid and mounts the home, so check that shape too.
docker run --rm --user 12345:12345 --read-only "$ref" omp --version
test "$(docker run --rm "$ref" omp --version)" = "omp/$(jq -r '.dependencies["@oh-my-pi/pi-coding-agent"]' tools/omp/package.json)"
