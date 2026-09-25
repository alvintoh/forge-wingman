# One image recipe for both Cloud Run deployables: CMD=surface (the default)
# or CMD=dispatcher. The runner is not an image — GitHub Actions runs it.

FROM oven/bun:1 AS web
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

FROM golang:1.27 AS build
ARG CMD=surface
# The build context excludes .git, so the caller passes the sha (gcloud run
# deploy --set-build-env-vars or a cloudbuild). Default "dev" matches the local
# build; it also means the dispatcher build below links without any symbol set.
ARG COMMIT_SHA=dev
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
# CGO off so the binary is static and runs on a distroless base.
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.commitSHA=${COMMIT_SHA}" -o /out/app ./cmd/${CMD}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
