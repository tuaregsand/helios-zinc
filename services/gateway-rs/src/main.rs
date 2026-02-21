use anyhow::{anyhow, Context};
use clap::Parser;
use futures_util::StreamExt;
use rdkafka::producer::{FutureProducer, FutureRecord};
use rdkafka::ClientConfig;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::{BTreeMap, HashMap};
use std::time::Duration;
use tracing::{info, warn};
use yellowstone_grpc_client::{ClientTlsConfig, GeyserGrpcClient};
use yellowstone_grpc_proto::prelude::{
    subscribe_update::UpdateOneof, CommitmentLevel, SubscribeRequest, SubscribeRequestFilterSlots,
    SubscribeRequestFilterTransactions, SubscribeUpdateTransaction,
};

#[derive(Parser, Debug)]
#[command(
    name = "helios-gateway",
    about = "Ingestion gateway: Solana stream -> commit log (Redpanda/Kafka)"
)]
struct Args {
    /// Kafka bootstrap servers
    #[arg(long, env = "HELIOS_KAFKA_BROKERS", default_value = "localhost:9092")]
    kafka_brokers: String,

    /// Output topic for raw transaction events
    #[arg(long, env = "HELIOS_RAW_TX_TOPIC", default_value = "helios.raw_tx")]
    out_topic: String,

    /// Source adapter to run: yellowstone | stdin
    #[arg(long, env = "HELIOS_GATEWAY_SOURCE", default_value = "yellowstone")]
    source: String,

    /// Solana cluster label to embed in emitted envelope keys
    #[arg(long, env = "HELIOS_CLUSTER", default_value = "mainnet-beta")]
    cluster: String,

    /// Yellowstone endpoint, for example https://...:443
    #[arg(long, env = "HELIOS_YELLOWSTONE_ENDPOINT")]
    yellowstone_endpoint: Option<String>,

    /// Optional provider auth token sent as x-token metadata
    #[arg(long, env = "HELIOS_YELLOWSTONE_X_TOKEN")]
    yellowstone_x_token: Option<String>,

    /// Subscription commitment: processed | confirmed | finalized
    #[arg(
        long,
        env = "HELIOS_YELLOWSTONE_COMMITMENT",
        default_value = "processed"
    )]
    yellowstone_commitment: String,

    /// Optional replay start slot
    #[arg(long, env = "HELIOS_YELLOWSTONE_FROM_SLOT")]
    yellowstone_from_slot: Option<u64>,

    /// Optional single-signature filter
    #[arg(long, env = "HELIOS_YELLOWSTONE_SIGNATURE")]
    yellowstone_signature: Option<String>,

    /// Account include filters (comma-separated program/account pubkeys)
    #[arg(
        long,
        env = "HELIOS_YELLOWSTONE_ACCOUNT_INCLUDE",
        value_delimiter = ','
    )]
    yellowstone_account_include: Vec<String>,

    /// Account exclude filters (comma-separated pubkeys)
    #[arg(
        long,
        env = "HELIOS_YELLOWSTONE_ACCOUNT_EXCLUDE",
        value_delimiter = ','
    )]
    yellowstone_account_exclude: Vec<String>,

    /// Account required filters (comma-separated pubkeys)
    #[arg(
        long,
        env = "HELIOS_YELLOWSTONE_ACCOUNT_REQUIRED",
        value_delimiter = ','
    )]
    yellowstone_account_required: Vec<String>,

    /// Include vote transactions
    #[arg(long, env = "HELIOS_YELLOWSTONE_INCLUDE_VOTE", default_value_t = false)]
    yellowstone_include_vote: bool,

    /// Include failed transactions
    #[arg(
        long,
        env = "HELIOS_YELLOWSTONE_INCLUDE_FAILED",
        default_value_t = true
    )]
    yellowstone_include_failed: bool,

    /// gRPC max inbound message size in bytes
    #[arg(
        long,
        env = "HELIOS_YELLOWSTONE_MAX_DECODING_MSG_SIZE",
        default_value_t = 50 * 1024 * 1024
    )]
    yellowstone_max_decoding_message_size: usize,

    /// Initial reconnect backoff in milliseconds
    #[arg(
        long,
        env = "HELIOS_YELLOWSTONE_RECONNECT_BACKOFF_MS",
        default_value_t = 1_000
    )]
    yellowstone_reconnect_backoff_ms: u64,

    /// Max reconnect backoff in milliseconds
    #[arg(
        long,
        env = "HELIOS_YELLOWSTONE_RECONNECT_BACKOFF_MAX_MS",
        default_value_t = 15_000
    )]
    yellowstone_reconnect_backoff_max_ms: u64,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
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

#[derive(Clone, Debug, Serialize, Deserialize)]
struct EventEnvelope {
    event_id_hex: String,
    key: EventKey,
    status: String,
    block_time_unix_ms: u64,
    source: String,
    source_seq: u64,
    raw_payload_json: serde_json::Value,
}

fn compute_event_id_hex(key: &EventKey) -> String {
    let canon = format!(
        "{}|{}|{}|{}|{}|{}|{}|{}|{}",
        key.cluster,
        key.slot,
        key.parent_slot,
        key.signature,
        key.tx_index,
        key.ix_index,
        key.inner_index,
        key.log_index,
        key.write_version
    );
    let mut hasher = Sha256::new();
    hasher.update(canon.as_bytes());
    hex::encode(hasher.finalize())
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let _ = dotenvy::from_filename(".env");
    let _ = dotenvy::from_filename("../../.env");

    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    let args = Args::parse();

    let producer: FutureProducer = ClientConfig::new()
        .set("bootstrap.servers", &args.kafka_brokers)
        .set("message.timeout.ms", "5000")
        .set("queue.buffering.max.messages", "200000")
        .create()
        .context("create kafka producer")?;

    info!(
        brokers = %args.kafka_brokers,
        topic = %args.out_topic,
        source = %args.source,
        "gateway started"
    );

    match args.source.as_str() {
        "stdin" => run_stdin(producer, &args.out_topic).await,
        "yellowstone" => run_yellowstone(producer, &args.out_topic, &args).await,
        other => anyhow::bail!("unknown --source: {other} (supported: stdin, yellowstone)"),
    }
}

async fn produce(
    producer: &FutureProducer,
    topic: &str,
    env: &EventEnvelope,
) -> anyhow::Result<()> {
    let payload = serde_json::to_vec(env)?;
    let key = &env.event_id_hex;

    let record = FutureRecord::to(topic).payload(&payload).key(key);
    match producer.send(record, Duration::from_secs(2)).await {
        Ok((_partition, _offset)) => Ok(()),
        Err((e, _)) => Err(anyhow!("delivery error: {e}")),
    }
}

async fn run_stdin(producer: FutureProducer, topic: &str) -> anyhow::Result<()> {
    use tokio::io::{self, AsyncBufReadExt};
    let stdin = io::stdin();
    let mut lines = io::BufReader::new(stdin).lines();

    let mut seq: u64 = 0;
    while let Some(line) = lines.next_line().await? {
        if line.trim().is_empty() {
            continue;
        }
        let mut raw: serde_json::Value = serde_json::from_str(&line)?;
        let key: EventKey = serde_json::from_value(raw["key"].take())?;
        let event_id_hex = compute_event_id_hex(&key);

        seq += 1;
        let env = EventEnvelope {
            event_id_hex,
            key,
            status: raw
                .get("status")
                .and_then(|v| v.as_str())
                .unwrap_or("processed")
                .to_string(),
            block_time_unix_ms: raw
                .get("block_time_unix_ms")
                .and_then(|v| v.as_u64())
                .unwrap_or(0),
            source: raw
                .get("source")
                .and_then(|v| v.as_str())
                .unwrap_or("stdin")
                .to_string(),
            source_seq: seq,
            raw_payload_json: raw,
        };

        produce(&producer, topic, &env).await?;
    }

    Ok(())
}

async fn run_yellowstone(producer: FutureProducer, topic: &str, args: &Args) -> anyhow::Result<()> {
    let endpoint = args
        .yellowstone_endpoint
        .clone()
        .or_else(|| std::env::var("HELIUS_LASERSTREAM_ENDPOINT").ok())
        .context(
            "--yellowstone-endpoint/HELIOS_YELLOWSTONE_ENDPOINT or HELIUS_LASERSTREAM_ENDPOINT is required",
        )?;

    let x_token = args
        .yellowstone_x_token
        .clone()
        .or_else(|| std::env::var("HELIUS_LASERSTREAM_API_KEY").ok());

    let commitment = parse_commitment(&args.yellowstone_commitment)?;
    let source = format!("yellowstone:{endpoint}");

    let mut backoff_ms = args.yellowstone_reconnect_backoff_ms.max(250);
    let backoff_max_ms = args.yellowstone_reconnect_backoff_max_ms.max(backoff_ms);

    loop {
        let result = run_yellowstone_session(
            &producer,
            topic,
            args,
            &endpoint,
            x_token.clone(),
            commitment,
            &source,
        )
        .await;

        match result {
            Ok(()) => warn!("yellowstone stream ended, reconnecting"),
            Err(err) => warn!(error = %err, "yellowstone stream failed, reconnecting"),
        }

        tokio::select! {
            _ = tokio::signal::ctrl_c() => {
                info!("shutdown signal received");
                return Ok(());
            }
            _ = tokio::time::sleep(Duration::from_millis(backoff_ms)) => {}
        }

        backoff_ms = backoff_ms.saturating_mul(2).min(backoff_max_ms);
    }
}

async fn run_yellowstone_session(
    producer: &FutureProducer,
    topic: &str,
    args: &Args,
    endpoint: &str,
    x_token: Option<String>,
    commitment: CommitmentLevel,
    source: &str,
) -> anyhow::Result<()> {
    let status = commitment_to_status(commitment);

    info!(
        endpoint = endpoint,
        commitment = status,
        "connecting yellowstone"
    );

    let mut builder = GeyserGrpcClient::build_from_shared(endpoint.to_string())?
        .connect_timeout(Duration::from_secs(10))
        .timeout(Duration::from_secs(30))
        .tcp_nodelay(true)
        .http2_adaptive_window(true)
        .max_decoding_message_size(args.yellowstone_max_decoding_message_size)
        .x_token(x_token)?;

    if endpoint.starts_with("https://") {
        builder = builder.tls_config(ClientTlsConfig::new().with_native_roots())?;
    }

    let mut client = builder.connect().await.context("connect to yellowstone")?;
    let mut stream = client
        .subscribe_once(build_subscribe_request(args, commitment))
        .await
        .context("subscribe to yellowstone stream")?;

    let mut seq: u64 = 0;
    let mut received_updates: u64 = 0;
    let mut slot_parents = BTreeMap::<u64, u64>::new();

    while let Some(update_result) = stream.next().await {
        let update = update_result.context("read yellowstone update")?;
        let created_at = update.created_at;
        let filters = update.filters;
        received_updates += 1;

        if received_updates % 10_000 == 0 {
            info!(received_updates, "yellowstone stream progress");
        }

        match update.update_oneof {
            Some(UpdateOneof::Slot(slot_update)) => {
                if let Some(parent) = slot_update.parent {
                    slot_parents.insert(slot_update.slot, parent);
                    if slot_parents.len() > 200_000 {
                        if let Some(oldest_slot) = slot_parents.keys().next().copied() {
                            slot_parents.remove(&oldest_slot);
                        }
                    }
                }
            }
            Some(UpdateOneof::Transaction(tx_update)) => {
                if let Some(env) = tx_update_to_envelope(
                    tx_update,
                    filters,
                    created_at,
                    EnvelopeBuildCtx {
                        cluster: &args.cluster,
                        status,
                        source,
                        seq: &mut seq,
                        slot_parents: &slot_parents,
                    },
                )? {
                    produce(producer, topic, &env).await?;
                }
            }
            Some(UpdateOneof::Ping(_)) | Some(UpdateOneof::Pong(_)) => {}
            _ => {}
        }
    }

    anyhow::bail!("yellowstone stream closed by server")
}

fn build_subscribe_request(args: &Args, commitment: CommitmentLevel) -> SubscribeRequest {
    let mut transactions = HashMap::new();
    transactions.insert(
        "helios_tx".to_string(),
        SubscribeRequestFilterTransactions {
            vote: Some(args.yellowstone_include_vote),
            failed: Some(args.yellowstone_include_failed),
            signature: args.yellowstone_signature.clone(),
            account_include: args.yellowstone_account_include.clone(),
            account_exclude: args.yellowstone_account_exclude.clone(),
            account_required: args.yellowstone_account_required.clone(),
        },
    );

    let mut slots = HashMap::new();
    slots.insert(
        "helios_slots".to_string(),
        SubscribeRequestFilterSlots {
            filter_by_commitment: Some(true),
            interslot_updates: Some(false),
        },
    );

    SubscribeRequest {
        accounts: HashMap::new(),
        slots,
        transactions,
        transactions_status: HashMap::new(),
        blocks: HashMap::new(),
        blocks_meta: HashMap::new(),
        entry: HashMap::new(),
        commitment: Some(commitment as i32),
        accounts_data_slice: vec![],
        ping: None,
        from_slot: args.yellowstone_from_slot,
    }
}

struct EnvelopeBuildCtx<'a> {
    cluster: &'a str,
    status: &'a str,
    source: &'a str,
    seq: &'a mut u64,
    slot_parents: &'a BTreeMap<u64, u64>,
}

fn tx_update_to_envelope(
    tx_update: SubscribeUpdateTransaction,
    filters: Vec<String>,
    created_at: Option<yellowstone_grpc_proto::prost_types::Timestamp>,
    ctx: EnvelopeBuildCtx<'_>,
) -> anyhow::Result<Option<EventEnvelope>> {
    let tx_info = match tx_update.transaction {
        Some(tx_info) => tx_info,
        None => return Ok(None),
    };

    let signature = bs58::encode(&tx_info.signature).into_string();
    let slot = tx_update.slot;
    let parent_slot = ctx
        .slot_parents
        .get(&slot)
        .copied()
        .unwrap_or_else(|| slot.saturating_sub(1));
    let tx_index = u32::try_from(tx_info.index).unwrap_or(u32::MAX);

    let key = EventKey {
        cluster: ctx.cluster.to_string(),
        slot,
        parent_slot,
        signature: signature.clone(),
        tx_index,
        ix_index: 0,
        inner_index: 0,
        log_index: 0,
        write_version: 0,
    };

    *ctx.seq += 1;
    Ok(Some(EventEnvelope {
        event_id_hex: compute_event_id_hex(&key),
        key,
        status: ctx.status.to_string(),
        block_time_unix_ms: timestamp_to_unix_ms(created_at.as_ref()),
        source: ctx.source.to_string(),
        source_seq: *ctx.seq,
        raw_payload_json: serde_json::json!({
            "kind": "transaction",
            "slot": slot,
            "index": tx_info.index,
            "signature": signature,
            "is_vote": tx_info.is_vote,
            "has_transaction": tx_info.transaction.is_some(),
            "has_meta": tx_info.meta.is_some(),
            "filters": filters
        }),
    }))
}

fn timestamp_to_unix_ms(ts: Option<&yellowstone_grpc_proto::prost_types::Timestamp>) -> u64 {
    match ts {
        Some(ts) => {
            let seconds = ts.seconds.max(0) as u64;
            let nanos = ts.nanos.max(0) as u64;
            seconds
                .saturating_mul(1_000)
                .saturating_add(nanos / 1_000_000)
        }
        None => 0,
    }
}

fn parse_commitment(value: &str) -> anyhow::Result<CommitmentLevel> {
    match value.to_ascii_lowercase().as_str() {
        "processed" => Ok(CommitmentLevel::Processed),
        "confirmed" => Ok(CommitmentLevel::Confirmed),
        "finalized" => Ok(CommitmentLevel::Finalized),
        other => anyhow::bail!(
            "unsupported commitment: {other}. expected one of: processed, confirmed, finalized"
        ),
    }
}

fn commitment_to_status(commitment: CommitmentLevel) -> &'static str {
    match commitment {
        CommitmentLevel::Processed => "processed",
        CommitmentLevel::Confirmed => "confirmed",
        CommitmentLevel::Finalized => "finalized",
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use rdkafka::consumer::{Consumer, StreamConsumer};
    use rdkafka::message::Message;
    use std::env;
    use std::time::{SystemTime, UNIX_EPOCH};
    use tokio::time::{timeout, Instant};
    use yellowstone_grpc_proto::prelude::SubscribeUpdateTransactionInfo;
    use yellowstone_grpc_proto::prost_types::Timestamp;

    fn test_args() -> Args {
        Args {
            kafka_brokers: "localhost:9092".to_string(),
            out_topic: "helios.raw_tx".to_string(),
            source: "yellowstone".to_string(),
            cluster: "mainnet-beta".to_string(),
            yellowstone_endpoint: Some("https://example.test:443".to_string()),
            yellowstone_x_token: None,
            yellowstone_commitment: "processed".to_string(),
            yellowstone_from_slot: Some(99),
            yellowstone_signature: Some("sig".to_string()),
            yellowstone_account_include: vec!["inc1".to_string(), "inc2".to_string()],
            yellowstone_account_exclude: vec!["exc1".to_string()],
            yellowstone_account_required: vec!["req1".to_string()],
            yellowstone_include_vote: false,
            yellowstone_include_failed: true,
            yellowstone_max_decoding_message_size: 1024,
            yellowstone_reconnect_backoff_ms: 1000,
            yellowstone_reconnect_backoff_max_ms: 8000,
        }
    }

    fn test_event_key() -> EventKey {
        EventKey {
            cluster: "mainnet-beta".to_string(),
            slot: 42,
            parent_slot: 41,
            signature: "abc".to_string(),
            tx_index: 7,
            ix_index: 1,
            inner_index: 2,
            log_index: 3,
            write_version: 9,
        }
    }

    fn integration_enabled() -> bool {
        matches!(env::var("HELIOS_IT_RUN").as_deref(), Ok("1"))
    }

    fn integration_kafka_env() -> anyhow::Result<(String, String)> {
        let brokers = env::var("HELIOS_IT_KAFKA_BROKERS")
            .context("HELIOS_IT_KAFKA_BROKERS is required when HELIOS_IT_RUN=1")?;
        let topic = env::var("HELIOS_IT_KAFKA_TOPIC")
            .context("HELIOS_IT_KAFKA_TOPIC is required when HELIOS_IT_RUN=1")?;
        if brokers.trim().is_empty() {
            anyhow::bail!("HELIOS_IT_KAFKA_BROKERS must not be empty");
        }
        if topic.trim().is_empty() {
            anyhow::bail!("HELIOS_IT_KAFKA_TOPIC must not be empty");
        }
        Ok((brokers, topic))
    }

    fn now_millis() -> u128 {
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock must be after unix epoch")
            .as_millis()
    }

    #[test]
    fn compute_event_id_hex_is_deterministic() {
        let key = test_event_key();
        let first = compute_event_id_hex(&key);
        let second = compute_event_id_hex(&key);

        assert_eq!(first, second);
        assert_eq!(first.len(), 64);
    }

    #[test]
    fn compute_event_id_hex_changes_with_key_content() {
        let key_a = test_event_key();
        let mut key_b = test_event_key();
        key_b.log_index += 1;

        assert_ne!(compute_event_id_hex(&key_a), compute_event_id_hex(&key_b));
    }

    #[test]
    fn parse_commitment_accepts_known_values() {
        assert_eq!(
            parse_commitment("processed").expect("processed should parse"),
            CommitmentLevel::Processed
        );
        assert_eq!(
            parse_commitment("confirmed").expect("confirmed should parse"),
            CommitmentLevel::Confirmed
        );
        assert_eq!(
            parse_commitment("finalized").expect("finalized should parse"),
            CommitmentLevel::Finalized
        );
        assert_eq!(
            parse_commitment("FiNaLiZeD").expect("case-insensitive parsing"),
            CommitmentLevel::Finalized
        );
    }

    #[test]
    fn parse_commitment_rejects_unknown_values() {
        let err = parse_commitment("rooted").expect_err("unknown value must fail");
        let msg = err.to_string();
        assert!(msg.contains("unsupported commitment"));
        assert!(msg.contains("processed"));
    }

    #[test]
    fn commitment_to_status_maps_enum_values() {
        assert_eq!(
            commitment_to_status(CommitmentLevel::Processed),
            "processed"
        );
        assert_eq!(
            commitment_to_status(CommitmentLevel::Confirmed),
            "confirmed"
        );
        assert_eq!(
            commitment_to_status(CommitmentLevel::Finalized),
            "finalized"
        );
    }

    #[test]
    fn timestamp_to_unix_ms_converts_and_handles_none() {
        let ts = Timestamp {
            seconds: 1700000000,
            nanos: 123_456_789,
        };
        assert_eq!(timestamp_to_unix_ms(Some(&ts)), 1_700_000_000_123);
        assert_eq!(timestamp_to_unix_ms(None), 0);
    }

    #[test]
    fn build_subscribe_request_applies_transaction_filters() {
        let args = test_args();
        let req = build_subscribe_request(&args, CommitmentLevel::Confirmed);

        assert_eq!(req.commitment, Some(CommitmentLevel::Confirmed as i32));
        assert_eq!(req.from_slot, Some(99));

        let tx_filter = req
            .transactions
            .get("helios_tx")
            .expect("helios_tx filter must exist");
        assert_eq!(tx_filter.vote, Some(false));
        assert_eq!(tx_filter.failed, Some(true));
        assert_eq!(tx_filter.signature.as_deref(), Some("sig"));
        assert_eq!(tx_filter.account_include, vec!["inc1", "inc2"]);
        assert_eq!(tx_filter.account_exclude, vec!["exc1"]);
        assert_eq!(tx_filter.account_required, vec!["req1"]);

        let slot_filter = req
            .slots
            .get("helios_slots")
            .expect("helios_slots filter must exist");
        assert_eq!(slot_filter.filter_by_commitment, Some(true));
        assert_eq!(slot_filter.interslot_updates, Some(false));
    }

    #[test]
    fn tx_update_to_envelope_builds_expected_payload() {
        let tx_update = SubscribeUpdateTransaction {
            transaction: Some(SubscribeUpdateTransactionInfo {
                signature: vec![1, 2, 3, 4],
                is_vote: false,
                transaction: None,
                meta: None,
                index: 7,
            }),
            slot: 100,
        };

        let mut slot_parents = BTreeMap::new();
        slot_parents.insert(100, 99);
        let mut seq = 0;

        let envelope = tx_update_to_envelope(
            tx_update,
            vec!["solana".to_string()],
            Some(Timestamp {
                seconds: 2,
                nanos: 250_000_000,
            }),
            EnvelopeBuildCtx {
                cluster: "mainnet-beta",
                status: "processed",
                source: "yellowstone:https://example.test:443",
                seq: &mut seq,
                slot_parents: &slot_parents,
            },
        )
        .expect("envelope should build")
        .expect("transaction update should produce envelope");

        assert_eq!(envelope.key.slot, 100);
        assert_eq!(envelope.key.parent_slot, 99);
        assert_eq!(envelope.key.tx_index, 7);
        assert_eq!(envelope.status, "processed");
        assert_eq!(envelope.block_time_unix_ms, 2250);
        assert_eq!(envelope.source_seq, 1);
        assert_eq!(seq, 1);
        assert_eq!(envelope.event_id_hex, compute_event_id_hex(&envelope.key));
        assert_eq!(envelope.raw_payload_json["kind"], "transaction");
        assert_eq!(envelope.raw_payload_json["slot"], 100);
        assert_eq!(envelope.raw_payload_json["is_vote"], false);
        assert_eq!(envelope.raw_payload_json["has_transaction"], false);
        assert_eq!(envelope.raw_payload_json["has_meta"], false);
        assert_eq!(
            envelope.key.signature,
            bs58::encode([1_u8, 2, 3, 4]).into_string()
        );
    }

    #[test]
    fn tx_update_to_envelope_returns_none_without_transaction_info() {
        let tx_update = SubscribeUpdateTransaction {
            transaction: None,
            slot: 50,
        };
        let mut seq = 0;
        let slot_parents = BTreeMap::new();

        let envelope = tx_update_to_envelope(
            tx_update,
            vec![],
            None,
            EnvelopeBuildCtx {
                cluster: "mainnet-beta",
                status: "processed",
                source: "yellowstone:https://example.test:443",
                seq: &mut seq,
                slot_parents: &slot_parents,
            },
        )
        .expect("call should succeed");

        assert!(envelope.is_none());
        assert_eq!(seq, 0);
    }

    #[tokio::test]
    async fn integration_produce_round_trip_with_kafka() -> anyhow::Result<()> {
        if !integration_enabled() {
            return Ok(());
        }
        let (brokers, topic) = integration_kafka_env()?;

        let producer: FutureProducer = ClientConfig::new()
            .set("bootstrap.servers", &brokers)
            .set("message.timeout.ms", "5000")
            .create()
            .context("create integration producer")?;

        let consumer_group = format!("helios-gateway-it-{}-{}", std::process::id(), now_millis());
        let consumer: StreamConsumer = ClientConfig::new()
            .set("bootstrap.servers", &brokers)
            .set("group.id", &consumer_group)
            .set("enable.auto.commit", "false")
            .set("auto.offset.reset", "latest")
            .create()
            .context("create integration consumer")?;
        consumer
            .subscribe(&[&topic])
            .context("subscribe integration consumer")?;

        tokio::time::sleep(Duration::from_millis(500)).await;

        let mut key = test_event_key();
        key.slot = now_millis() as u64;
        key.parent_slot = key.slot.saturating_sub(1);
        key.signature = format!("it-{}", key.slot);
        let envelope = EventEnvelope {
            event_id_hex: compute_event_id_hex(&key),
            key,
            status: "processed".to_string(),
            block_time_unix_ms: 0,
            source: "integration".to_string(),
            source_seq: 1,
            raw_payload_json: serde_json::json!({"kind":"integration"}),
        };

        produce(&producer, &topic, &envelope).await?;

        let deadline = Instant::now() + Duration::from_secs(20);
        let mut stream = consumer.stream();
        loop {
            let wait = deadline.saturating_duration_since(Instant::now());
            if wait.is_zero() {
                anyhow::bail!(
                    "timed out waiting for event_id {} on topic {}",
                    envelope.event_id_hex,
                    topic
                );
            }

            let message = timeout(wait, stream.next())
                .await
                .context("timeout waiting for kafka message")?
                .ok_or_else(|| anyhow!("consumer stream ended"))?
                .context("consumer error")?;

            let payload = message
                .payload()
                .ok_or_else(|| anyhow!("message payload missing"))?;
            let decoded: EventEnvelope =
                serde_json::from_slice(payload).context("decode kafka payload envelope")?;
            if decoded.event_id_hex == envelope.event_id_hex {
                assert_eq!(decoded.key.signature, envelope.key.signature);
                assert_eq!(decoded.status, "processed");
                break;
            }
        }

        Ok(())
    }
}
