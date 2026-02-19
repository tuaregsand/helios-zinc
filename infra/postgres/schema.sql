-- Helios metadata/state store (Postgres)

CREATE TABLE IF NOT EXISTS consumer_offsets (
  consumer_group TEXT NOT NULL,
  topic          TEXT NOT NULL,
  partition      INT  NOT NULL,
  offset         BIGINT NOT NULL,
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer_group, topic, partition)
);

CREATE TABLE IF NOT EXISTS finality_watermarks (
  cluster  TEXT NOT NULL,
  status   TEXT NOT NULL CHECK (status IN ('processed','confirmed','finalized')),
  slot     BIGINT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (cluster, status)
);

-- Which program parser versions are available.
-- In production: program-specific parsers are shipped as WASM with strict versioning.
CREATE TABLE IF NOT EXISTS parser_registry (
  program_id TEXT NOT NULL,
  version    INT  NOT NULL,
  wasm_sha256 TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (program_id, version)
);

-- Replay/backfill jobs (slot ranges).
CREATE TABLE IF NOT EXISTS replay_jobs (
  job_id       UUID PRIMARY KEY,
  cluster      TEXT NOT NULL,
  slot_start   BIGINT NOT NULL,
  slot_end     BIGINT NOT NULL,
  status       TEXT NOT NULL CHECK (status IN ('queued','running','failed','done')),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  error        TEXT
);
