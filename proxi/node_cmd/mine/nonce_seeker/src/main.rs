//! Reference external nonce seeker for `proxi node mine`
//! (kb/external_nonce_seeker.md in the Proxima repository).
//!
//! It polls the miner for the current job, searches nonces on every core and
//! posts every one that solves the job and beats the best so far, until the
//! job ends: the miner keeps a job open for the whole round and answers each
//! result with the beat the next must pass. The miner verifies every nonce,
//! builds and submits the transaction.
//!
//! Two kinds of job. Version 2, transaction-hash work (kb/library_upgrade6_mining.md):
//! the job carries the transit's ID preimage without the nonce, and a solution has K
//! trailing zero bits in the last 8 bytes of BLAKE2b-256(message || nonce); the seeker
//! needs no key. Version 1, VRF work, before library upgrade 6: the VRF output needs the
//! wallet's secret scalar, so those jobs are taken only with a key, and refused without.

mod client;
mod key;

use client::{Client, Verdict};
use proxima_vrf::Prover;
use rand::Rng;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, RwLock};
use std::thread;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

const SEED_ENV: &str = "NONCE_SEEKER_SEED";
/// how long one job poll is held open on the miner's side
const POLL_WAIT_MS: u64 = 5000;
/// attempts between two looks at the shared job
const BATCH: u64 = 256;
const REPORT_EVERY: Duration = Duration::from_secs(1);
const RATE_LINE_EVERY: Duration = Duration::from_secs(10);
const RETRY_MIN: Duration = Duration::from_secs(1);
const RETRY_MAX: Duration = Duration::from_secs(30);

struct Config {
    proxi: String,
    token: Option<String>,
    key_file: PathBuf,
    threads: usize,
    name: String,
}

/// What a job's work is computed over.
enum Work {
    /// VRF work: the VRF message without its nonce
    Vrf { alpha_prefix: Vec<u8> },
    /// transaction-hash work: the hash state after the message, which the nonce ends
    Hash { state: blake2b_simd::State },
}

/// One search target, as the workers see it. Immutable once installed but for
/// the beat, which every answer of the miner lowers; a new job is a new value,
/// so workers notice the change by the id.
struct Job {
    id: u64,
    slot: u64,
    work: Work,
    k: u32,
    beat: RwLock<Option<Vec<u8>>>,
    deadline: Instant,
    solved: AtomicBool,
}

struct Shared {
    job: RwLock<Option<Arc<Job>>>,
    /// attempts made and not yet reported to the miner
    attempts: AtomicU64,
}

fn main() {
    let cfg = match parse_args() {
        Ok(c) => c,
        Err(e) => {
            eprintln!("{e}");
            eprintln!("usage: nonce_seeker --proxi URL [--token T] [--key-file FILE] [--threads N] [--name NAME]");
            std::process::exit(2);
        }
    };
    // the key is needed only for VRF jobs; transaction-hash jobs need none
    let prover = match load_seed(&cfg) {
        Ok(seed) => Some(Arc::new(Prover::from_seed(&seed))),
        Err(e) => {
            log(&format!("no key ({e}): only transaction-hash jobs (version 2) are taken"));
            None
        }
    };
    let client = Arc::new(Client::new(&cfg.proxi, cfg.token.clone()));
    let shared = Arc::new(Shared { job: RwLock::new(None), attempts: AtomicU64::new(0) });

    log(&format!(
        "nonce seeker '{}': {}, {} threads, miner at {}",
        cfg.name,
        prover.as_ref().map_or("no key".to_string(), |p| format!("key {}", hex::encode(p.public_key()))),
        cfg.threads,
        cfg.proxi
    ));

    for tid in 0..cfg.threads {
        let (shared, prover, client, name) = (shared.clone(), prover.clone(), client.clone(), cfg.name.clone());
        let threads = cfg.threads;
        thread::spawn(move || worker(tid, threads, &shared, prover.as_deref(), &client, &name));
    }
    {
        let (shared, client, name) = (shared.clone(), client.clone(), cfg.name.clone());
        thread::spawn(move || reporter(&shared, &client, &name));
    }
    poller(&shared, &client, prover.as_ref().map(|p| *p.public_key()));
}

fn parse_args() -> Result<Config, String> {
    let mut cfg = Config {
        proxi: String::new(),
        token: None,
        key_file: PathBuf::from("proxima.key"),
        threads: thread::available_parallelism().map(|n| n.get()).unwrap_or(1),
        name: format!("seeker-{}", std::process::id()),
    };
    let mut args = std::env::args().skip(1);
    while let Some(a) = args.next() {
        let mut value = |flag: &str| args.next().ok_or(format!("{flag} needs a value"));
        match a.as_str() {
            "--proxi" => cfg.proxi = value("--proxi")?,
            "--token" => cfg.token = Some(value("--token")?),
            "--key-file" => cfg.key_file = PathBuf::from(value("--key-file")?),
            "--threads" => {
                cfg.threads = value("--threads")?.parse().map_err(|e| format!("--threads: {e}"))?;
                if cfg.threads == 0 {
                    return Err("--threads must be at least 1".into());
                }
            }
            "--name" => cfg.name = value("--name")?,
            other => return Err(format!("unknown argument {other}")),
        }
    }
    if cfg.proxi.is_empty() {
        return Err("--proxi URL is required (the address 'proxi node mine' serves jobs on)".into());
    }
    Ok(cfg)
}

/// The key file wins over the environment seed when both are present.
fn load_seed(cfg: &Config) -> Result<[u8; 32], String> {
    if cfg.key_file.exists() {
        return key::load_seed(&cfg.key_file);
    }
    if let Ok(s) = std::env::var(SEED_ENV) {
        return key::seed_from_hex(&s);
    }
    Err(format!("no key file at {} and {SEED_ENV} is not set", cfg.key_file.display()))
}

/// Keeps the shared job current. Errors are retried forever with backoff: a
/// miner restarts, a network blips, and the seeker must simply be there when
/// the jobs come back.
fn poller(shared: &Shared, client: &Client, our_pk: Option<[u8; 32]>) {
    let mut after = 0u64;
    let mut retry = RETRY_MIN;
    loop {
        let wire = match client.get_job(after, POLL_WAIT_MS) {
            Ok(w) => {
                retry = RETRY_MIN;
                w
            }
            Err(e) => {
                log(&format!("cannot reach the miner: {e}; retrying in {retry:?}"));
                thread::sleep(retry);
                retry = (retry * 2).min(RETRY_MAX);
                continue;
            }
        };
        if wire.id == after {
            continue; // the long-poll expired with the job unchanged
        }
        after = wire.id;
        if wire.id == 0 {
            log("no job: the miner is waiting");
            *shared.job.write().unwrap() = None;
            continue;
        }
        match install(&wire, our_pk.as_ref()) {
            Ok(job) => {
                log(&format!(
                    "job {} ({}): slot {}, K={}{}, {:.1}s left",
                    job.id,
                    match job.work {
                        Work::Hash { .. } => "transaction-hash work",
                        Work::Vrf { .. } => "VRF work",
                    },
                    job.slot,
                    job.k,
                    if job.beat.read().unwrap().is_some() { " (contested: must beat the best known solution)" } else { "" },
                    job.deadline.saturating_duration_since(Instant::now()).as_secs_f64()
                ));
                *shared.job.write().unwrap() = Some(Arc::new(job));
            }
            Err(e) => {
                log(&format!("REFUSING job {}: {e}", wire.id));
                *shared.job.write().unwrap() = None;
            }
        }
    }
}

fn install(w: &client::JobWire, our_pk: Option<&[u8; 32]>) -> Result<Job, String> {
    let work = match w.version {
        2 => {
            let message = hex::decode(&w.message).map_err(|e| format!("message: {e}"))?;
            if w.nonce_offset != message.len() {
                return Err(format!("nonce offset {} is not the message's end ({})", w.nonce_offset, message.len()));
            }
            Work::Hash { state: hash_state(&message) }
        }
        0 | 1 => {
            let Some(pk) = our_pk else {
                return Err("a VRF job needs the wallet key (--key-file)".into());
            };
            if w.pubkey != hex::encode(pk) {
                return Err(format!("the miner mines under key {} but this seeker holds {}", w.pubkey, hex::encode(pk)));
            }
            Work::Vrf { alpha_prefix: hex::decode(&w.alpha_prefix).map_err(|e| format!("alpha_prefix: {e}"))? }
        }
        v => return Err(format!("job version {v} is not known to this seeker: update it")),
    };
    let beat = if w.beat.is_empty() { None } else { Some(hex::decode(&w.beat).map_err(|e| format!("beat: {e}"))?) };
    Ok(Job {
        id: w.id,
        slot: w.slot,
        work,
        k: w.k,
        beat: RwLock::new(beat),
        deadline: Instant::now() + Duration::from_millis(w.ttl_ms.max(0) as u64),
        solved: AtomicBool::new(false),
    })
}

/// The BLAKE2b-256 state after the message, as the Go miner keeps it.
fn hash_state(message: &[u8]) -> blake2b_simd::State {
    let mut st = blake2b_simd::Params::new().hash_length(32).to_state();
    st.update(message);
    st
}

/// One transaction-hash attempt: the hash of the message and the nonce, the work's
/// trailing zero bits (in its last 8 bytes) and its rank key, those 8 bytes followed by
/// bytes 6 to 23 of the hash, compared as bytes against the beat.
fn hash_attempt(state: &blake2b_simd::State, nonce: u64) -> ([u8; 32], u32, Vec<u8>) {
    let mut st = state.clone();
    st.update(&nonce.to_be_bytes());
    let mut sum = [0u8; 32];
    sum.copy_from_slice(st.finalize().as_bytes());
    let mut key = Vec::with_capacity(26);
    key.extend_from_slice(&sum[24..32]);
    key.extend_from_slice(&sum[6..24]);
    (sum, trailing_zero_bits(&sum[24..32]), key)
}

/// One search thread. Each thread starts a job at its own random nonce and
/// walks its own residue class modulo the thread count, so threads never
/// repeat each other and neither do separate seekers on the same job.
fn worker(tid: usize, threads: usize, shared: &Shared, prover: Option<&Prover>, client: &Client, name: &str) {
    let mut rng = rand::thread_rng();
    let mut working: u64 = 0; // id of the job the nonce walk belongs to
    let mut nonce: u64 = 0;
    let mut alpha: Vec<u8> = Vec::new();
    loop {
        let job = shared.job.read().unwrap().clone();
        let Some(job) = job else {
            thread::sleep(Duration::from_millis(100));
            continue;
        };
        if job.solved.load(Ordering::Relaxed) || Instant::now() >= job.deadline {
            thread::sleep(Duration::from_millis(100));
            continue;
        }
        if job.id != working {
            working = job.id;
            nonce = rng.gen::<u64>().wrapping_add(tid as u64);
            if let Work::Vrf { alpha_prefix } = &job.work {
                alpha.clear();
                alpha.extend_from_slice(alpha_prefix);
                alpha.extend_from_slice(&[0u8; 8]);
            }
        }
        let mut done = 0u64;
        let bar = job.beat.read().unwrap().clone();
        for _ in 0..BATCH {
            nonce = nonce.wrapping_add(threads as u64);
            done += 1;
            let found: Option<Vec<u8>> = match &job.work {
                Work::Hash { state } => {
                    let (sum, tz, key) = hash_attempt(state, nonce);
                    (tz >= job.k && bar.as_ref().map_or(true, |b| key < *b)).then(|| sum.to_vec())
                }
                Work::Vrf { .. } => {
                    let Some(prover) = prover else { break };
                    let n = alpha.len();
                    alpha[n - 8..].copy_from_slice(&nonce.to_be_bytes());
                    let (beta, _) = prover.output(&alpha);
                    (trailing_zero_bits(&beta) >= job.k && bar.as_ref().map_or(true, |b| beta.as_slice() < b.as_slice())).then(|| beta.to_vec())
                }
            };
            if let Some(beta) = found {
                let attempts = shared.attempts.swap(0, Ordering::Relaxed) + done;
                done = 0;
                submit(&job, nonce, &beta, attempts, client, name);
                break;
            }
        }
        shared.attempts.fetch_add(done, Ordering::Relaxed);
    }
}

fn submit(job: &Job, nonce: u64, beta: &[u8], attempts: u64, client: &Client, name: &str) {
    match client.post_result(job.id, nonce, beta, attempts, name) {
        Ok(Verdict::Accepted(beat)) => {
            set_beat(job, &beat);
            log(&format!("job {}: SOLVED with nonce {nonce:#018x}, accepted; searching for a better one", job.id));
        }
        Ok(Verdict::Beaten(beat)) => set_beat(job, &beat),
        Ok(Verdict::Stale) => {
            job.solved.store(true, Ordering::Relaxed);
            log(&format!("job {}: solved, but the miner has moved on", job.id));
        }
        Ok(Verdict::Rejected(reason)) => {
            // a disagreement about the work itself: keep searching, but say so
            log(&format!("job {}: REJECTED nonce {nonce:#018x}: {reason}", job.id));
        }
        Err(e) => log(&format!("job {}: cannot post the solution: {e}", job.id)),
    }
}

/// Lowers the job's beat to what the miner answered; a malformed one is left
/// for the miner to refuse.
fn set_beat(job: &Job, hex_beat: &str) {
    if let Ok(b) = hex::decode(hex_beat) {
        let mut cur = job.beat.write().unwrap();
        if cur.as_ref().map_or(true, |c| b < *c) {
            *cur = Some(b);
        }
    }
}

/// Reports the attempts once a second; the miner sizes its windows from them.
/// A failed report keeps its attempts for the next one.
fn reporter(shared: &Shared, client: &Client, name: &str) {
    let mut carried = 0u64;
    let mut window_start = Instant::now();
    let mut window_attempts = 0u64;
    let mut last_error = Instant::now() - RATE_LINE_EVERY;
    loop {
        thread::sleep(REPORT_EVERY);
        let n = shared.attempts.swap(0, Ordering::Relaxed) + carried;
        window_attempts += n - carried;
        let id = shared.job.read().unwrap().as_ref().map_or(0, |j| j.id);
        carried = match client.post_report(id, n, name) {
            Ok(()) => 0,
            Err(e) => {
                if last_error.elapsed() >= RATE_LINE_EVERY {
                    log(&format!("cannot report attempts: {e}"));
                    last_error = Instant::now();
                }
                n
            }
        };
        if window_start.elapsed() >= RATE_LINE_EVERY {
            let rate = window_attempts as f64 / window_start.elapsed().as_secs_f64();
            if id != 0 {
                log(&format!("job {id}: {rate:.0} H/s"));
            }
            window_start = Instant::now();
            window_attempts = 0;
        }
    }
}

/// Zero bits at the least significant end of a big-endian value, the last byte
/// first: the definition the mine lock enforces.
fn trailing_zero_bits(beta: &[u8]) -> u32 {
    let mut n = 0;
    for &b in beta.iter().rev() {
        if b == 0 {
            n += 8;
        } else {
            return n + b.trailing_zeros();
        }
    }
    n
}

fn log(msg: &str) {
    let secs = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
    println!("{:02}:{:02}:{:02} {msg}", secs / 3600 % 24, secs / 60 % 60, secs % 60);
}

#[cfg(test)]
mod tests {
    use super::{hash_attempt, hash_state, trailing_zero_bits};

    #[test]
    fn trailing_zeros_count_from_the_end() {
        assert_eq!(trailing_zero_bits(&[0xff, 0x00]), 8);
        assert_eq!(trailing_zero_bits(&[0x00, 0x01]), 0);
        assert_eq!(trailing_zero_bits(&[0x01, 0x00, 0x00]), 16);
        assert_eq!(trailing_zero_bits(&[0x80]), 7);
        assert_eq!(trailing_zero_bits(&[0x00; 3]), 24);
    }

    /// The kept state, cloned and finished with the nonce, is the plain BLAKE2b-256 of
    /// message and nonce, for messages ending before, at and after a 128-byte block
    /// boundary; and it equals what the Go miner computes for the same input
    /// (TestHashTemplateVectors in proxi/node_cmd/mine, which prints these values).
    #[test]
    fn hash_attempt_matches_plain_blake2b_and_go() {
        for (len, want) in super::tests::GO_VECTORS {
            let message: Vec<u8> = (0..*len).map(|i| i as u8).collect();
            let nonce = 0x0102030405060708u64;
            let (sum, _, key) = hash_attempt(&hash_state(&message), nonce);
            let mut whole = message.clone();
            whole.extend_from_slice(&nonce.to_be_bytes());
            let plain = blake2b_simd::Params::new().hash_length(32).hash(&whole);
            assert_eq!(&sum[..], plain.as_bytes(), "message length {len}");
            assert_eq!(hex::encode(sum), *want, "message length {len}: Go and Rust disagree");
            assert_eq!(&key[..8], &sum[24..32]);
            assert_eq!(&key[8..], &sum[6..24]);
        }
    }

    /// Message lengths and the BLAKE2b-256 of message || nonce, computed by the Go miner.
    pub const GO_VECTORS: &[(usize, &str)] = &[
        (100, "b7af2281301d649b893e3673679fdf7e61d74dd7347365c17518dc69253506c8"),
        (119, "a3687092de02840d3e8c30e5025d320ac20cc1feb342ba1fba78c60c41c07d3c"),
        (120, "bae38c5df1e42849a52915f23f758ec092485b76e3e5c8ec4e6204c2bef1e6ca"),
        (127, "a1443e852985373cdba3b004be8ab05e20610b869f8389f919b01f537e0c9646"),
        (128, "aaf45eb2daad328c85f92c61f87154454b369f6fbb7c6bffff43d1a305518259"),
        (129, "3f8e4f06a0c13899d168aeb96a93e857a65141a0cf4dee9748324029ecccf17c"),
        (200, "82eba8169dd58a9e6042487be103689d728a36cff1035e2ab1da07196dec3d3d"),
        (247, "20b9eef755e709fd11307bffc52e773d05c058a63134f65166af9c1744d8febc"),
        (248, "ddddc25892c55fe25c48c9660493f7830aa7ae43f29efabcfce2a329c71e2583"),
        (256, "307a1a5b84ff17649608b11a3c701f04ef39548b42e9c432ae1d889fbc56b296"),
        (300, "3a8a0f43b3c3d96fe5466a2f1c751ff06308d08ed409b5a5f4d7d069ef14c604"),
    ];
}
