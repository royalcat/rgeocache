# syntax=docker/dockerfile:1

# Stage 1: build
FROM --platform=$BUILDPLATFORM rust:1-trixie AS build

ARG TARGETARCH
ARG TARGETVARIANT

# protoc is required by build.rs (buffa-build / prost-build).
RUN apt-get update \
    && apt-get install -y --no-install-recommends protobuf-compiler

# Map TARGETARCH to a Rust target triple, install the matching cross C
# toolchain (zstd-sys, pulled in by tantivy, is the only C code in the tree)
# and record the cargo/cc environment in /cross-env.sh for the build step.
#
# On amd64, TARGETVARIANT (v2/v3/v4, from e.g. --platform linux/amd64/v3)
# selects the x86-64 microarchitecture level for both the Rust crates
# (RUSTFLAGS) and the C code (CFLAGS); plain amd64 stays at the baseline.
RUN set -eux; \
    cpu=""; cflags=""; \
    case "$TARGETARCH" in \
      amd64) \
        case "$TARGETVARIANT" in \
          ""|v1) ;; \
          v2|v3|v4) cpu="x86-64-$TARGETVARIANT"; cflags="-march=$cpu" ;; \
          *) echo "unsupported TARGETVARIANT for amd64: $TARGETVARIANT" >&2; exit 1 ;; \
        esac; \
        triple=x86_64-unknown-linux-gnu; cc="" ;; \
      arm64)   triple=aarch64-unknown-linux-gnu; cc=aarch64-linux-gnu-gcc; \
               apt-get update; \
               apt-get install -y --no-install-recommends gcc-aarch64-linux-gnu libc6-dev-arm64-cross; \
               rm -rf /var/lib/apt/lists/* ;; \
      riscv64) triple=riscv64gc-unknown-linux-gnu; cc=riscv64-linux-gnu-gcc; \
               apt-get update; \
               apt-get install -y --no-install-recommends gcc-riscv64-linux-gnu libc6-dev-riscv64-cross; \
               rm -rf /var/lib/apt/lists/* ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    rustup target add "$triple"; \
    up=$(echo "$triple" | tr 'a-z-' 'A-Z_'); \
    uu=$(echo "$triple" | tr '-' '_'); \
    { echo "export RUST_TARGET=$triple"; \
      if [ -n "$cpu" ]; then \
        echo "export RUSTFLAGS=\"-C target-cpu=$cpu\""; \
        echo "export CFLAGS=\"$cflags\""; \
      fi; \
      if [ -n "$cc" ]; then \
        echo "export CARGO_TARGET_${up}_LINKER=$cc"; \
        echo "export CC_${uu}=$cc"; \
        echo "export AR_${uu}=$(echo "$cc" | sed 's/gcc$/ar/')"; \
      fi; \
    } > /cross-env.sh

WORKDIR /app
COPY Cargo.toml Cargo.lock build.rs ./
COPY proto/ ./proto/
COPY src/ ./src/
# `src/server.rs` embeds `web/fgeocode-demo.html` for the demo page, so the
# synced copy of the shared page has to be part of this context.
COPY web/ ./web/
RUN --mount=type=cache,target=/usr/local/cargo/registry,sharing=locked \
    --mount=type=cache,target=/build/target,id=target-$TARGETARCH-${TARGETVARIANT:-v1},sharing=locked \
    set -eux; \
    . /cross-env.sh; \
    export CARGO_TARGET_DIR=/build/target; \
    cargo build --release --target "$RUST_TARGET" --features demo-page; \
    mkdir -p /out; \
    cp "/build/target/$RUST_TARGET/release/rgeocache-server" /out/rgeocache-server

# Stage 2: runtime
FROM debian:trixie-slim

COPY --from=build /out/rgeocache-server /rgeocache-server

VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/rgeocache-server"]
CMD ["--points", "/data/cache.rgc", "--listen", "0.0.0.0:8080"]
