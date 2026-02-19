-- Helios OLAP store (ClickHouse)

-- Raw transaction events as received from Geyser/Yellowstone/RPC.
-- ReplacingMergeTree gives us idempotent semantics when we insert the same event_id again.
CREATE TABLE IF NOT EXISTS tx_raw (
  event_id        FixedString(32),
  cluster         LowCardinality(String),
  status          Enum8('processed' = 1, 'confirmed' = 2, 'finalized' = 3),
  slot            UInt64,
  parent_slot     UInt64,
  block_time      Nullable(DateTime64(3, 'UTC')),
  signature       String,
  write_version   UInt64,
  tx_index        UInt32,
  raw_payload     String,
  ingested_at     DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = ReplacingMergeTree(write_version)
ORDER BY (event_id)
SETTINGS index_granularity = 8192;

-- Decoded instructions (normalized, query-friendly).
CREATE TABLE IF NOT EXISTS ix_decoded (
  event_id        FixedString(32),
  cluster         LowCardinality(String),
  status          Enum8('processed' = 1, 'confirmed' = 2, 'finalized' = 3),
  slot            UInt64,
  signature       String,
  tx_index        UInt32,
  ix_index        UInt32,
  inner_index     UInt32,
  program_id      FixedString(44),
  accounts        Array(FixedString(44)),
  json            String,
  ingested_at     DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = ReplacingMergeTree()
ORDER BY (cluster, slot, signature, tx_index, ix_index, inner_index);

-- Wallet-to-wallet interaction edges.
CREATE TABLE IF NOT EXISTS wallet_edges_1h (
  bucket_start DateTime64(0, 'UTC'),
  src          FixedString(44),
  dst          FixedString(44),
  program_id   FixedString(44),
  n            UInt32,
  volume_lamports UInt64,
  first_seen_slot UInt64,
  last_seen_slot  UInt64
)
ENGINE = SummingMergeTree()
PARTITION BY toYYYYMMDD(bucket_start)
ORDER BY (bucket_start, src, dst, program_id);

-- Feature store (time-windowed features for detection).
CREATE TABLE IF NOT EXISTS wallet_features_1h (
  bucket_start DateTime64(0, 'UTC'),
  wallet       FixedString(44),
  feature_key  LowCardinality(String),
  feature_value Float64
)
ENGINE = ReplacingMergeTree()
PARTITION BY toYYYYMMDD(bucket_start)
ORDER BY (bucket_start, wallet, feature_key);
