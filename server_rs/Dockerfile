# syntax=docker/dockerfile:1

# Stage 1: build
FROM --platform=$BUILDPLATFORM rust:1-bookworm AS build

RUN apt-get update && apt-get install -y protobuf-compiler

WORKDIR /app
COPY Cargo.toml Cargo.lock build.rs ./
COPY proto/ ./proto/
COPY src/ ./src/
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/build/target/ \
    cargo build --release && \
    cp target/release/rgeocache-server /tmp/rgeocache-server


# Stage 2: runtime
FROM debian:bookworm-slim

COPY --from=build /tmp/rgeocache-server /rgeocache-server

VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/rgeocache-server"]
CMD ["--points", "/data/cache.rgc", "--listen", "0.0.0.0:8080"]
