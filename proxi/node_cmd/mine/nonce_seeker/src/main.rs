//! Reference external nonce seeker for `proxi node mine`
//! (kb/external_nonce_seeker.md in the Proxima repository).
//!
//! It polls the miner for the current job, searches nonces on every core and
//! posts the one that solves the job. It holds the wallet key, because the
//! VRF output needs the secret scalar, and nothing else: the miner verifies
//! the nonce, completes the proof, builds and submits the transaction.

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

/// One search target, as the workers see it. Immutable once installed; a new
/// job is a new value, so workers notice the change by the id.
struct Job {
    id: u64,
    slot: u64,
    alpha_prefix: Vec<u8>,
    k: u32,
    beat: Option<[u8; 64]>,
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
    let seed = match load_seed(&cfg) {
        Ok(s) => s,
        Err(e) => {
            eprintln!("cannot load the key: {e}");
            std::process::exit(1);
        }
    };
    let prover = Arc::new(Prover::from_seed(&seed));
    let client = Arc::new(Client::new(&cfg.proxi, cfg.token.clone()));
    let shared = Arc::new(Shared { job: RwLock::new(None), attempts: AtomicU64::new(0) });

    log(&format!(
        "nonce seeker '{}': key {}, {} threads, miner at {}",
        cfg.name,
        hex::encode(prover.public_key()),
        cfg.threads,
        cfg.proxi
    ));

    for tid in 0..cfg.threads {
        let (shared, prover, client, name) = (shared.clone(), prover.clone(), client.clone(), cfg.name.clone());
        let threads = cfg.threads;
        thread::spawn(move || worker(tid, threads, &shared, &prover, &client, &name));
    }
    {
        let (shared, client, name) = (shared.clone(), client.clone(), cfg.name.clone());
        thread::spawn(move || reporter(&shared, &client, &name));
    }
    poller(&shared, &client, prover.public_key());
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
fn poller(shared: &Shared, client: &Client, our_pk: &[u8; 32]) {
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
        match install(&wire, our_pk) {
            Ok(job) => {
                log(&format!(
                    "job {}: slot {}, K={}{}, {:.1}s left",
                    job.id,
                    job.slot,
                    job.k,
                    if job.beat.is_some() { " (contested: must beat the best known output)" } else { "" },
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

fn install(w: &client::JobWire, our_pk: &[u8; 32]) -> Result<Job, String> {
    if w.pubkey != hex::encode(our_pk) {
        return Err(format!("the miner mines under key {} but this seeker holds {}", w.pubkey, hex::encode(our_pk)));
    }
    let alpha_prefix = hex::decode(&w.alpha_prefix).map_err(|e| format!("alpha_prefix: {e}"))?;
    let beat = if w.beat.is_empty() {
        None
    } else {
        let b = hex::decode(&w.beat).map_err(|e| format!("beat: {e}"))?;
        Some(<[u8; 64]>::try_from(b.as_slice()).map_err(|_| "beat is not 64 bytes".to_string())?)
    };
    Ok(Job {
        id: w.id,
        slot: w.slot,
        alpha_prefix,
        k: w.k,
        beat,
        deadline: Instant::now() + Duration::from_millis(w.ttl_ms.max(0) as u64),
        solved: AtomicBool::new(false),
    })
}

/// One search thread. Each thread starts a job at its own random nonce and
/// walks its own residue class modulo the thread count, so threads never
/// repeat each other and neither do separate seekers under the same key.
fn worker(tid: usize, threads: usize, shared: &Shared, prover: &Prover, client: &Client, name: &str) {
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
            alpha.clear();
            alpha.extend_from_slice(&job.alpha_prefix);
            alpha.extend_from_slice(&[0u8; 8]);
        }
        let n = alpha.len();
        let mut done = 0u64;
        for _ in 0..BATCH {
            nonce = nonce.wrapping_add(threads as u64);
            alpha[n - 8..].copy_from_slice(&nonce.to_be_bytes());
            let (beta, _) = prover.output(&alpha);
            done += 1;
            if trailing_zero_bits(&beta) >= job.k && job.beat.map_or(true, |b| beta < b) {
                let attempts = shared.attempts.swap(0, Ordering::Relaxed) + done;
                done = 0;
                submit(&job, nonce, &beta, attempts, client, name);
                break;
            }
        }
        shared.attempts.fetch_add(done, Ordering::Relaxed);
    }
}

fn submit(job: &Job, nonce: u64, beta: &[u8; 64], attempts: u64, client: &Client, name: &str) {
    match client.post_result(job.id, nonce, beta, attempts, name) {
        Ok(Verdict::Accepted) => {
            job.solved.store(true, Ordering::Relaxed);
            log(&format!("job {}: SOLVED with nonce {nonce:#018x}, accepted", job.id));
        }
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

/// Zero bits at the least significant end of beta, the last byte first: the
/// definition the mine lock enforces.
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
    use super::trailing_zero_bits;

    #[test]
    fn trailing_zeros_count_from_the_end() {
        assert_eq!(trailing_zero_bits(&[0xff, 0x00]), 8);
        assert_eq!(trailing_zero_bits(&[0x00, 0x01]), 0);
        assert_eq!(trailing_zero_bits(&[0x01, 0x00, 0x00]), 16);
        assert_eq!(trailing_zero_bits(&[0x80]), 7);
        assert_eq!(trailing_zero_bits(&[0x00; 3]), 24);
    }
}
