//! Line tool for the Go equivalence test of util/keystore: one request per
//! stdin line, one answer per stdout line. Strings that may be empty or hold
//! spaces (passphrase, holder ID, hint) travel as hex, "-" for empty. Errors
//! are answered as "ERR <message>". Files are relative to the working
//! directory, which is also where the passphrase file is looked for.
//!
//!   encrypt <path> <priv> <pub> <pass> <holder> <hint> -> OK
//!   unencrypted <path> <priv> <pub> <holder>           -> OK
//!   load <path> <pass>      -> <priv> <0|1 encrypted> <pub> <holder> <hint>
//!   encrypt_file <in> <out> <pass> <hint>              -> OK
//!   decrypt_file <in> <out> <pass>                     -> OK
//!   passfile <path>         -> <pass> | NONE
//!   iskeystore <path>       -> 0 | 1

use proxima_keystore::{is_keystore_file, Keystore, KEY_TYPE_ED25519};
use std::io::{self, BufRead, Write};
use std::path::Path;

fn text(parts: &[&str], i: usize) -> Result<String, String> {
    let s = parts.get(i).ok_or_else(|| format!("missing argument {i}"))?;
    if *s == "-" {
        return Ok(String::new());
    }
    let b = hex::decode(s).map_err(|e| format!("argument {i}: {e}"))?;
    String::from_utf8(b).map_err(|e| format!("argument {i}: {e}"))
}

fn bytes(parts: &[&str], i: usize) -> Result<Vec<u8>, String> {
    let s = parts.get(i).ok_or_else(|| format!("missing argument {i}"))?;
    hex::decode(s).map_err(|e| format!("argument {i}: {e}"))
}

fn path<'a>(parts: &[&'a str], i: usize) -> Result<&'a Path, String> {
    parts.get(i).map(|s| Path::new(*s)).ok_or_else(|| format!("missing argument {i}"))
}

fn enc(s: &str) -> String {
    if s.is_empty() {
        "-".to_string()
    } else {
        hex::encode(s)
    }
}

fn answer(line: &str) -> Result<String, String> {
    let p: Vec<&str> = line.split_whitespace().collect();
    let e = |x: proxima_keystore::Error| x.to_string();
    match p.first().copied().unwrap_or("") {
        "encrypt" => {
            let mut ks = Keystore::encrypt(KEY_TYPE_ED25519, &bytes(&p, 2)?, &bytes(&p, 3)?, &text(&p, 4)?, &text(&p, 5)?).map_err(e)?;
            ks.hint = text(&p, 6)?;
            ks.save_to_file(path(&p, 1)?).map_err(e)?;
            Ok("OK".into())
        }
        "unencrypted" => {
            let ks = Keystore::new_unencrypted(KEY_TYPE_ED25519, &bytes(&p, 2)?, &bytes(&p, 3)?, &text(&p, 4)?).map_err(e)?;
            ks.save_to_file(path(&p, 1)?).map_err(e)?;
            Ok("OK".into())
        }
        "load" => {
            let ks = Keystore::load_from_file(path(&p, 1)?).map_err(e)?;
            let private = ks.get_private_key(&text(&p, 2)?).map_err(e)?;
            Ok(format!(
                "{} {} {} {} {}",
                hex::encode(private),
                u8::from(ks.is_encrypted()),
                hex::encode(ks.public_key_bytes().map_err(e)?),
                enc(&ks.holder_id),
                enc(&ks.hint)
            ))
        }
        "encrypt_file" => {
            let ks = Keystore::load_from_file(path(&p, 1)?).map_err(e)?;
            ks.encrypt_keystore(&text(&p, 3)?, &text(&p, 4)?).map_err(e)?.save_to_file(path(&p, 2)?).map_err(e)?;
            Ok("OK".into())
        }
        "decrypt_file" => {
            let ks = Keystore::load_from_file(path(&p, 1)?).map_err(e)?;
            ks.decrypt_keystore(&text(&p, 3)?).map_err(e)?.save_to_file(path(&p, 2)?).map_err(e)?;
            Ok("OK".into())
        }
        "passfile" => {
            let ks = Keystore::load_from_file(path(&p, 1)?).map_err(e)?;
            Ok(ks.read_passphrase_file().map_or("NONE".to_string(), |s| enc(&s)))
        }
        "iskeystore" => Ok(u8::from(is_keystore_file(path(&p, 1)?)).to_string()),
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
