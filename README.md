# Helios

sol infra concept: real-time ingestion + indexing under peak load. designed around the followinng themes: distributed architecture, backpressure, retries/replay, and no-loss/no-dup processing. analytics side focus on large wallet/tx behavior patterns, including coordinated multi-wallet detectionn

infra so far: yellowstone/geyser stream source, kafka/redpanda buffer, clickhouse + postgres storage. services split: rust for ingestion/decode path, go for api/replay/control jobs, keeping it modular so pipeline logic can change without rewriting everything.

early draft!!

working on a solana data pipeline + analytics idea. more details soon ;;