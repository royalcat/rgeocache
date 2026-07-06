# syntax=docker/dockerfile:1

#   Basic build (no PGO):
#   docker build -t rgeocache-server .
#
#   PGO build (with pre-collected profile):
#   docker build --secret id=pgoprofile,src=./merged.profdata -t rgeocache-server .

# Stage 1: build
FROM --platform=$BUILDPLATFORM rust:1-slim-bookworm AS build

RUN apt-get update && apt-get install -y \
    musl-tools \
    protobuf-compiler \
    && rm -rf /var/lib/apt/lists/*

# Determine the Rust target triple for static musl linking.
# Building for musl produces a fully-static binary that runs in scratch.
ARG TARGETARCH
RUN case "${TARGETARCH}" in \
      amd64)  echo "x86_64-unknown-linux-musl"  > /tmp/rust_target ;; \
      arm64)  echo "aarch64-unknown-linux-musl" > /tmp/rust_target ;; \
      *)      echo "Unsupported architecture: ${TARGETARCH}"; exit 1 ;; \
    esac && \
    rustup target add "$(cat /tmp/rust_target)"

WORKDIR /app

# Copy manifests and vendored proto files first (layer caching for deps).
COPY Cargo.toml Cargo.lock build.rs ./
COPY proto/ ./proto/

# Cache dependency builds.
RUN --mount=type=cache,target=/app/target,sharing=locked \
    --mount=type=cache,target=/usr/local/cargo/registry,sharing=locked \
    RUST_TARGET="$(cat /tmp/rust_target)" && \
    cargo build --release --target "${RUST_TARGET}" --bin rgeocache-server || true

# Copy source and build the final binary.
COPY src/ ./src/

# Build the binary
# If a PGO profile is provided via build secret, use it.
# Otherwise, build a regular release binary.

RUN --mount=type=cache,target=/app/target,sharing=locked \
    --mount=type=cache,target=/usr/local/cargo/registry,sharing=locked \
    --mount=type=secret,id=pgoprofile,dst=/tmp/pgo/merged.profdata,required=false \
    RUST_TARGET="$(cat /tmp/rust_target)" && \
    if [ -s /tmp/pgo/merged.profdata ]; then \
      echo "=== Building with PGO profile ===" && \
      RUSTFLAGS="-C profile-use=/tmp/pgo/merged.profdata -C llvm-args=-pgo-warn-missing-function" \
      cargo build --release --target "${RUST_TARGET}" --bin rgeocache-server; \
    else \
      echo "=== Building without PGO profile ===" && \
      cargo build --release --target "${RUST_TARGET}" --bin rgeocache-server; \
    fi && \
    cp "target/${RUST_TARGET}/release/rgeocache-server" /tmp/rgeocache-server

# Stage 2: runtime
FROM scratch

COPY --from=build /tmp/rgeocache-server /rgeocache-server

VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/rgeocache-server"]
CMD ["--points", "/data/cache.rgc", "--listen", "0.0.0.0:8080"]
