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
    ca-certificates \
  && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /out
COPY services/decoder-rs/Cargo.toml services/decoder-rs/Cargo.lock ./services/decoder-rs/
RUN mkdir -p services/decoder-rs/src && echo 'fn main(){}' > services/decoder-rs/src/main.rs
RUN cargo build --release --manifest-path services/decoder-rs/Cargo.toml && rm -rf services/decoder-rs/src
COPY services/decoder-rs ./services/decoder-rs
RUN cargo build --release --manifest-path services/decoder-rs/Cargo.toml && cp services/decoder-rs/target/release/helios-decoder /out/decoder

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /out/decoder /app/decoder
ENTRYPOINT ["/app/decoder"]
