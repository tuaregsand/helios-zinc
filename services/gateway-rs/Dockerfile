# syntax=docker/dockerfile:1.7
FROM --platform=$TARGETPLATFORM rust:slim-bookworm AS builder
WORKDIR /src
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    cmake \
    pkg-config \
    libcurl4-openssl-dev \
    libssl-dev \
    libsasl2-dev \
    zlib1g-dev \
    libzstd-dev \
    liblz4-dev \
    protobuf-compiler \
    ca-certificates \
  && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /out
COPY services/gateway-rs/Cargo.toml services/gateway-rs/Cargo.lock ./services/gateway-rs/
RUN mkdir -p services/gateway-rs/src && echo 'fn main(){}' > services/gateway-rs/src/main.rs
RUN cargo build --release --manifest-path services/gateway-rs/Cargo.toml && rm -rf services/gateway-rs/src
COPY services/gateway-rs ./services/gateway-rs
RUN cargo build --release --manifest-path services/gateway-rs/Cargo.toml && cp services/gateway-rs/target/release/helios-gateway /out/gateway

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /out/gateway /app/gateway
ENTRYPOINT ["/app/gateway"]
