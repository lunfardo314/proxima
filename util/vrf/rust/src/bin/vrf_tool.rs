//! Line tool for the Go equivalence test of util/vrf: one request per stdin
//! line, one answer per stdout line. Hex everywhere; "-" stands for an empty
//! alpha. Errors are answered as "ERR <message>".
//!
//!   output <seed> <alpha>        -> beta
//!   prove <seed> <alpha>         -> pi
//!   prove64 <private_key> <alpha>-> pi   (64-byte Go-style private key)
//!   verify <pk> <alpha> <pi>     -> beta | ERR
//!   hash <pi>                    -> beta | ERR

use proxima_vrf::{proof_to_hash, prove, verify, Prover};
use std::io::{self, BufRead, Write};

fn arg(parts: &[&str], i: usize) -> Result<Vec<u8>, String> {
    let s = parts.get(i).ok_or_else(|| format!("missing argument {i}"))?;
    if *s == "-" {
        return Ok(Vec::new());
    }
    hex::decode(s).map_err(|e| format!("argument {i}: {e}"))
}

fn seed(parts: &[&str], i: usize) -> Result<[u8; 32], String> {
    let b = arg(parts, i)?;
    <[u8; 32]>::try_from(b.as_slice()).map_err(|_| "seed must be 32 bytes".to_string())
}

fn answer(line: &str) -> Result<String, String> {
    let parts: Vec<&str> = line.split_whitespace().collect();
    match parts.first().copied().unwrap_or("") {
        "output" => Ok(hex::encode(Prover::from_seed(&seed(&parts, 1)?).output(&arg(&parts, 2)?).0)),
        "prove" => Ok(hex::encode(Prover::from_seed(&seed(&parts, 1)?).prove(&arg(&parts, 2)?))),
        "prove64" => prove(&arg(&parts, 1)?, &arg(&parts, 2)?).map(hex::encode).map_err(|e| e.to_string()),
        "verify" => verify(&arg(&parts, 1)?, &arg(&parts, 2)?, &arg(&parts, 3)?).map(hex::encode).map_err(|e| e.to_string()),
        "hash" => proof_to_hash(&arg(&parts, 1)?).map(hex::encode).map_err(|e| e.to_string()),
        other => Err(format!("unknown command {other:?}")),
    }
}

fn main() {
    let stdin = io::stdin();
    let mut out = io::stdout().lock();
    for line in stdin.lock().lines() {
        let line = match line {
            Ok(l) => l,
            Err(_) => break,
        };
        let reply = match answer(&line) {
            Ok(r) => r,
            Err(e) => format!("ERR {e}"),
        };
        if writeln!(out, "{reply}").and_then(|_| out.flush()).is_err() {
            break;
        }
    }
}
