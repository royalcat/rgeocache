#build container
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /app

COPY go.mod ./
COPY go.sum ./


RUN --mount=type=cache,mode=0777,target=/go/pkg/mod go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,mode=0777,target=/go/pkg/mod CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /rgeocache ./cmd

# run container
FROM scratch

COPY --from=build /rgeocache /app/rgeocache
ENV PATH="/app:${PATH}"
# The forward geocoding index defaults to a temporary directory; scratch has
# none, so expose a writable /tmp volume (or pass --fgeocode-index).
ENV TMPDIR="/tmp"

VOLUME [ "/data", "/tmp" ]
ENTRYPOINT [ "rgeocache", "serve", "--points", "/data/points-data.gob" ]
