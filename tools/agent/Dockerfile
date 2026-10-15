# The unattended agent's image: omp from the committed lockfile, the bun omp
# runs on, node, and git. Build context is the repo root; only the manifest and
# lockfile pair is copied. The Cloud Run recipe is the root Dockerfile; this one
# shares nothing with it.

FROM oven/bun:1.4.2-debian@sha256:4f6e31d1a54d6a3dd312daef655fc998101b5043d52e12592ac293ef04b9bc73 AS bun

FROM node:22-bookworm-slim@sha256:c3de60bf2f9dd0ac6370e6117950ff62d6e339527e7472301c9c78a017978392

LABEL org.opencontainers.image.source="https://github.com/alvintoh/forge-wingman"

COPY --from=bun /usr/local/bin/bun /usr/local/bin/bun

RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY tools/omp/package.json tools/omp/bun.lock /opt/agent/omp/
# The package caches live in /tmp and go in the same layer, so the image keeps
# only the installed trees. copyfile, because bun's default links into its cache.
RUN bun install --frozen-lockfile --cwd /opt/agent/omp --backend=copyfile --cache-dir=/tmp/bun-cache \
    && rm -rf /tmp/bun-cache

# Any uid must read and run these: the caller picks --user. No USER, no
# ENTRYPOINT and no HOME: the caller passes the command and mounts the home.
RUN chmod -R a+rX /opt/agent
ENV PATH="/opt/agent/omp/node_modules/.bin:${PATH}"
