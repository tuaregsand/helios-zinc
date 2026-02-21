use anyhow::Context;
use clap::Parser;
use clickhouse::{Client, Row};
use futures_util::StreamExt;
use rdkafka::consumer::{CommitMode, Consumer, StreamConsumer};
use rdkafka::message::Message;
use rdkafka::ClientConfig;
use serde::{Deserialize, Serialize};
use std::time::Duration;
use tokio::time::Instant;
use tracing::{info, warn};

#[derive(Parser, Debug)]
#[command(
    name = "helios-decoder",
    about = "Decode/normalize worker: commit log -> OLAP/OLTP sinks"
)]
struct Args {
    #[arg(long, env = "HELIOS_KAFKA_BROKERS", default_value = "localhost:9092")]
    kafka_brokers: String,

    #[arg(long, env = "HELIOS_RAW_TX_TOPIC", default_value = "helios.raw_tx")]
    in_topic: String,

    #[arg(long, env = "HELIOS_DECODER_GROUP", default_value = "helios-decoder")]
    consumer_group: String,

    #[arg(
        long,
        env = "HELIOS_CLICKHOUSE_URL",
        default_value = "http://localhost:8123"
    )]
    clickhouse_url: String,

    #[arg(long, env = "HELIOS_CLICKHOUSE_DB", default_value = "helios")]
    clickhouse_db: String,

    #[arg(long, env = "HELIOS_CLICKHOUSE_USER", default_value = "helios")]
    clickhouse_user: String,

    #[arg(long, env = "HELIOS_CLICKHOUSE_PASSWORD", default_value = "helios")]
    clickhouse_password: String,

    /// Max events per insert batch
    #[arg(long, env = "HELIOS_DECODER_BATCH_SIZE", default_value_t = 2000)]
    batch_size: usize,

    /// Max time before we flush a partial batch
    #[arg(long, env = "HELIOS_DECODER_BATCH_FLUSH_MS", default_value_t = 250)]
    batch_flush_ms: u64,
}

#[allow(dead_code)]
#[derive(Clone, Debug, Deserialize)]
struct EventKey {
    cluster: String,
    slot: u64,
    parent_slot: u64,
    signature: String,
    tx_index: u32,
    ix_index: u32,
    inner_index: u32,
    log_index: u32,
    write_version: u64,
}

#[allow(dead_code)]
#[derive(Clone, Debug, Deserialize)]
struct EventEnvelope {
    event_id_hex: String,
    key: EventKey,
    status: String,
    block_time_unix_ms: u64,
    source: String,
    source_seq: u64,
    raw_payload_json: serde_json::Value,
}

#[derive(Row, Serialize, Debug)]
struct TxRawRow {
    event_id: Vec<u8>,
    cluster: String,
    status: i8,
    slot: u64,
    parent_slot: u64,
    block_time: Option<String>,
    signature: String,
    write_version: u64,
    tx_index: u32,
    raw_payload: String,
}

fn status_to_enum8(status: &str) -> i8 {
    match status {
        "processed" => 1,
        "confirmed" => 2,
        "finalized" => 3,
        _ => 1,
    }
}

fn event_id_bytes(hex_str: &str) -> anyhow::Result<Vec<u8>> {
    let b = hex::decode(hex_str).context("decode event_id_hex")?;
    if b.len() != 32 {
        anyhow::bail!("event_id must be 32 bytes, got {}", b.len());
    }
    Ok(b)
}

fn envelope_to_row(env: EventEnvelope) -> anyhow::Result<TxRawRow> {
    Ok(TxRawRow {
        event_id: event_id_bytes(&env.event_id_hex)?,
        cluster: env.key.cluster,
        status: status_to_enum8(&env.status),
        slot: env.key.slot,
        parent_slot: env.key.parent_slot,
        block_time: None,
        signature: env.key.signature,
        write_version: env.key.write_version,
        tx_index: env.key.tx_index,
        raw_payload: env.raw_payload_json.to_string(),
    })
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let _ = dotenvy::from_filename(".env");
    let _ = dotenvy::from_filename("../../.env");

    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    let args = Args::parse();

    let consumer: StreamConsumer = ClientConfig::new()
        .set("bootstrap.servers", &args.kafka_brokers)
        .set("group.id", &args.consumer_group)
        .set("enable.auto.commit", "false")
        .set("auto.offset.reset", "earliest")
        .create()
        .context("create kafka consumer")?;

    consumer.subscribe(&[&args.in_topic]).context("subscribe")?;

    let ch = Client::default()
        .with_url(args.clickhouse_url)
        .with_database(args.clickhouse_db)
        .with_user(args.clickhouse_user)
        .with_password(args.clickhouse_password);

    info!("decoder started");

    let mut batch: Vec<TxRawRow> = Vec::with_capacity(args.batch_size);
    let mut batch_first = Instant::now();

    let mut stream = consumer.stream();
    loop {
        tokio::select! {
          maybe_msg = stream.next() => {
            let msg = match maybe_msg {
              Some(Ok(m)) => m,
              Some(Err(e)) => { warn!("kafka error: {e}"); continue; }
              None => break,
            };

            let payload = match msg.payload() {
              Some(p) => p,
              None => continue,
            };

            let env: EventEnvelope = match serde_json::from_slice(payload) {
              Ok(v) => v,
              Err(e) => {
                warn!("bad payload: {e}");
                consumer.commit_message(&msg, CommitMode::Async)?;
                continue;
              }
            };

            let row = envelope_to_row(env)?;

            batch.push(row);

            let should_flush = batch.len() >= args.batch_size || batch_first.elapsed().as_millis() as u64 >= args.batch_flush_ms;
            if should_flush {
              flush_batch(&ch, &consumer, &batch).await?;
              batch.clear();
              batch_first = Instant::now();
            }
          }
          _ = tokio::time::sleep(Duration::from_millis(args.batch_flush_ms)) => {
            if !batch.is_empty() {
              flush_batch(&ch, &consumer, &batch).await?;
              batch.clear();
              batch_first = Instant::now();
            }
          }
        }
    }

    Ok(())
}

async fn flush_batch(
    ch: &Client,
    consumer: &StreamConsumer,
    rows: &[TxRawRow],
) -> anyhow::Result<()> {
    insert_rows(ch, "tx_raw", rows).await?;

    // Commit offsets only after sink success.
    consumer.commit_consumer_state(CommitMode::Async)?;
    info!(n = rows.len(), "flushed batch");
    Ok(())
}

async fn insert_rows(ch: &Client, table: &str, rows: &[TxRawRow]) -> anyhow::Result<()> {
    let mut insert = ch.insert(table).context("create insert")?;
    for r in rows {
        insert.write(r).await?;
    }
    insert.end().await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::env;
    use std::time::{SystemTime, UNIX_EPOCH};

    fn test_envelope(event_id_hex: &str, status: &str) -> EventEnvelope {
        EventEnvelope {
            event_id_hex: event_id_hex.to_string(),
            key: EventKey {
                cluster: "mainnet-beta".to_string(),
                slot: 42,
                parent_slot: 41,
                signature: "sig123".to_string(),
                tx_index: 7,
                ix_index: 0,
                inner_index: 0,
                log_index: 0,
                write_version: 9,
            },
            status: status.to_string(),
            block_time_unix_ms: 0,
            source: "yellowstone".to_string(),
            source_seq: 1,
            raw_payload_json: serde_json::json!({"kind":"transaction"}),
        }
    }

    fn integration_enabled() -> bool {
        matches!(env::var("HELIOS_IT_RUN").as_deref(), Ok("1"))
    }

    fn integration_clickhouse_env() -> anyhow::Result<(String, String, String, String)> {
        let url = env::var("HELIOS_IT_CLICKHOUSE_URL")
            .context("HELIOS_IT_CLICKHOUSE_URL is required when HELIOS_IT_RUN=1")?;
        if url.trim().is_empty() {
            anyhow::bail!("HELIOS_IT_CLICKHOUSE_URL must not be empty");
        }
        let db = env::var("HELIOS_IT_CLICKHOUSE_DB").unwrap_or_else(|_| "default".to_string());
        let user = env::var("HELIOS_IT_CLICKHOUSE_USER").unwrap_or_else(|_| "default".to_string());
        let password = env::var("HELIOS_IT_CLICKHOUSE_PASSWORD").unwrap_or_default();
        Ok((url, db, user, password))
    }

    fn now_millis() -> u128 {
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock must be after unix epoch")
            .as_millis()
    }

    #[test]
    fn status_to_enum8_maps_known_values() {
        assert_eq!(status_to_enum8("processed"), 1);
        assert_eq!(status_to_enum8("confirmed"), 2);
        assert_eq!(status_to_enum8("finalized"), 3);
    }

    #[test]
    fn status_to_enum8_defaults_unknown_to_processed() {
        assert_eq!(status_to_enum8("unknown"), 1);
    }

    #[test]
    fn event_id_bytes_accepts_valid_32_byte_hash() {
        let input = "ab".repeat(32);
        let bytes = event_id_bytes(&input).expect("valid hash should decode");
        assert_eq!(bytes.len(), 32);
        assert!(bytes.iter().all(|b| *b == 0xab));
    }

    #[test]
    fn event_id_bytes_rejects_non_hex_input() {
        assert!(event_id_bytes("zz").is_err());
    }

    #[test]
    fn event_id_bytes_rejects_wrong_length() {
        assert!(event_id_bytes("ab").is_err());
    }

    #[test]
    fn envelope_to_row_maps_fields() {
        let env = test_envelope(&"01".repeat(32), "confirmed");
        let row = envelope_to_row(env).expect("envelope should map");

        assert_eq!(row.event_id.len(), 32);
        assert_eq!(row.cluster, "mainnet-beta");
        assert_eq!(row.status, 2);
        assert_eq!(row.slot, 42);
        assert_eq!(row.parent_slot, 41);
        assert_eq!(row.signature, "sig123");
        assert_eq!(row.write_version, 9);
        assert_eq!(row.tx_index, 7);
        assert_eq!(row.raw_payload, "{\"kind\":\"transaction\"}");
    }

    #[test]
    fn envelope_to_row_propagates_invalid_event_id() {
        let env = test_envelope("abcd", "processed");
        assert!(envelope_to_row(env).is_err());
    }

    #[test]
    fn args_parse_accepts_overrides() {
        let args = Args::try_parse_from([
            "helios-decoder",
            "--kafka-brokers",
            "kafka.internal:9092",
            "--in-topic",
            "helios.raw_tx",
            "--consumer-group",
            "decoder-a",
            "--clickhouse-url",
            "http://ch:8123",
            "--clickhouse-db",
            "helios",
            "--clickhouse-user",
            "user1",
            "--clickhouse-password",
            "pass1",
            "--batch-size",
            "4096",
            "--batch-flush-ms",
            "500",
        ])
        .expect("args should parse");

        assert_eq!(args.kafka_brokers, "kafka.internal:9092");
        assert_eq!(args.in_topic, "helios.raw_tx");
        assert_eq!(args.consumer_group, "decoder-a");
        assert_eq!(args.clickhouse_url, "http://ch:8123");
        assert_eq!(args.clickhouse_db, "helios");
        assert_eq!(args.clickhouse_user, "user1");
        assert_eq!(args.clickhouse_password, "pass1");
        assert_eq!(args.batch_size, 4096);
        assert_eq!(args.batch_flush_ms, 500);
    }

    #[test]
    fn args_parse_rejects_invalid_batch_size() {
        let result = Args::try_parse_from(["helios-decoder", "--batch-size", "nope"]);
        assert!(result.is_err());
    }

    #[tokio::test]
    async fn integration_insert_rows_into_clickhouse() -> anyhow::Result<()> {
        if !integration_enabled() {
            return Ok(());
        }
        let (url, db, user, password) = integration_clickhouse_env()?;

        let ch = Client::default()
            .with_url(url)
            .with_database(db)
            .with_user(user)
            .with_password(password);

        let table = format!("tx_raw_it_{}_{}", std::process::id(), now_millis());
        let create_sql = format!(
            "CREATE TABLE {} (
                event_id FixedString(32),
                cluster String,
                status Int8,
                slot UInt64,
                parent_slot UInt64,
                block_time Nullable(String),
                signature String,
                write_version UInt64,
                tx_index UInt32,
                raw_payload String
            ) ENGINE = MergeTree ORDER BY (slot, tx_index, signature)",
            table
        );
        ch.query(&create_sql)
            .execute()
            .await
            .context("create integration table")?;

        let row = envelope_to_row(test_envelope(&"ab".repeat(32), "finalized"))?;
        insert_rows(&ch, &table, &[row])
            .await
            .context("insert integration row")?;

        let count_sql = format!("SELECT count() FROM {}", table);
        let count: u64 = ch
            .query(&count_sql)
            .fetch_one()
            .await
            .context("query integration row count")?;

        let drop_sql = format!("DROP TABLE IF EXISTS {}", table);
        ch.query(&drop_sql)
            .execute()
            .await
            .context("drop integration table")?;

        assert_eq!(count, 1);
        Ok(())
    }
}
