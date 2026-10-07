//! The wallet key: the same keystore file `proxi` uses, read with the Proxima
//! keystore library and unlocked in the order `proxi` unlocks it, so the miner
//! and the seeker share one key file and one passphrase procedure.

use proxima_keystore::{Keystore, KEY_TYPE_ED25519};
use std::path::Path;

pub const PASSPHRASE_ENV: &str = "PROXIMA_KEY_PASSPHRASE";

/// The 32-byte seed from a keystore file, prompting for the passphrase when
/// the file is encrypted and no passphrase source is set.
pub fn load_seed(path: &Path) -> Result<[u8; 32], String> {
    let ks = Keystore::load_from_file(path).map_err(|e| e.to_string())?;
    if ks.key_type != KEY_TYPE_ED25519 {
        return Err(format!("unsupported key type {} in {}", ks.key_type, path.display()));
    }
    let passphrase = if ks.is_encrypted() { passphrase_for(&ks, path)? } else { String::new() };
    // checks the stored public key against the private key as well
    let private = ks.get_private_key(&passphrase).map_err(|e| e.to_string())?;
    let mut seed = [0u8; 32];
    seed.copy_from_slice(&private[..32]);
    Ok(seed)
}

/// The order `proxi` uses: a file in the working directory named after the
/// holder ID, then the environment variable, then a no-echo prompt.
fn passphrase_for(ks: &Keystore, path: &Path) -> Result<String, String> {
    if let Some(p) = ks.read_passphrase_file() {
        return Ok(p);
    }
    if let Ok(p) = std::env::var(PASSPHRASE_ENV) {
        if !p.is_empty() {
            return Ok(p);
        }
    }
    let hint = if ks.hint.is_empty() { String::new() } else { format!(" (hint: {})", ks.hint) };
    rpassword::prompt_password(format!("Enter passphrase for '{}'{hint}: ", path.display()))
        .map_err(|e| format!("cannot read the passphrase: {e}; set {PASSPHRASE_ENV} or a passphrase file when there is no terminal"))
}

/// A bare seed from the environment, for images that carry no key file.
pub fn seed_from_hex(s: &str) -> Result<[u8; 32], String> {
    let b = hex::decode(s.trim()).map_err(|e| format!("seed is not hex: {e}"))?;
    <[u8; 32]>::try_from(b.as_slice()).map_err(|_| format!("seed has {} bytes, expected 32", b.len()))
}
