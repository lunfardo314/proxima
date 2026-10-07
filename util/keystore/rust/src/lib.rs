//! Proxima key storage in a unified JSON format, encrypted (Argon2id KDF +
//! AES-256-GCM) or not. The Rust twin of the Go package `util/keystore`: the
//! same file format, the same functions and the same checks, so a key file
//! written by either side is read by the other. The Go package's tests check
//! that against this crate's `keystore_tool` binary.
//!
//! The public key and the holder ID are stored in clear for identification.

use aes_gcm::aead::{Aead, KeyInit};
use aes_gcm::{Aes256Gcm, Key, Nonce};
use argon2::{Algorithm, Argon2, Params, Version};
use rand::RngCore;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha512};
use std::path::Path;

pub const KEY_TYPE_ED25519: u32 = 0;
pub const VERSION: u32 = 1;
pub const DEFAULT_KEY_FILE: &str = "proxima.key";

const ARGON_TIME: u32 = 3;
const ARGON_MEMORY: u32 = 64 * 1024; // KiB
const ARGON_THREADS: u32 = 4;
const SALT_SIZE: usize = 16;
const NONCE_SIZE: usize = 12;
const KEY_SIZE: usize = 32;
const ED25519_PRIVATE_KEY_SIZE: usize = 64;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Error(pub String);

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for Error {}

fn err<T>(msg: impl Into<String>) -> Result<T, Error> {
    Err(Error(msg.into()))
}

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct KdfParams {
    pub time: u32,
    pub memory: u32,
    pub threads: u32,
    /// hex
    pub salt: String,
}

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct CryptoData {
    pub cipher: String,
    pub kdf: String,
    pub kdf_params: KdfParams,
    /// hex
    pub nonce: String,
    /// hex, includes the GCM tag
    pub ciphertext: String,
}

/// The key file. Encrypted keystores have `crypto` and no `private_key`;
/// unencrypted ones the reverse. Field names and presence follow the Go struct.
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct Keystore {
    pub version: u32,
    pub key_type: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub crypto: Option<CryptoData>,
    /// hex, present when not encrypted
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub private_key: String,
    /// hex
    pub public_key: String,
    #[serde(default)]
    pub holder_id: String,
    /// optional passphrase hint of an encrypted keystore
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub hint: String,
}

impl Keystore {
    /// An unencrypted keystore. holder_id is an opaque identifier (the account).
    pub fn new_unencrypted(key_type: u32, private_key: &[u8], pubkey: &[u8], holder_id: &str) -> Result<Keystore, Error> {
        if private_key.is_empty() {
            return err("private key must not be empty");
        }
        if pubkey.is_empty() {
            return err("public key must not be empty");
        }
        Ok(Keystore {
            version: VERSION,
            key_type,
            crypto: None,
            private_key: hex::encode(private_key),
            public_key: hex::encode(pubkey),
            holder_id: holder_id.to_string(),
            hint: String::new(),
        })
    }

    /// A keystore with the private key encrypted under the passphrase.
    pub fn encrypt(key_type: u32, private_key: &[u8], pubkey: &[u8], passphrase: &str, holder_id: &str) -> Result<Keystore, Error> {
        if passphrase.is_empty() {
            return err("passphrase must not be empty");
        }
        Ok(Keystore {
            version: VERSION,
            key_type,
            crypto: Some(encrypt_bytes(private_key, passphrase)?),
            private_key: String::new(),
            public_key: hex::encode(pubkey),
            holder_id: holder_id.to_string(),
            hint: String::new(),
        })
    }

    /// The encrypted form of an unencrypted keystore.
    pub fn encrypt_keystore(&self, passphrase: &str, hint: &str) -> Result<Keystore, Error> {
        if self.is_encrypted() {
            return err("keystore is already encrypted");
        }
        if passphrase.is_empty() {
            return err("passphrase must not be empty");
        }
        let private = hex::decode(&self.private_key).map_err(|e| Error(format!("invalid private key hex: {e}")))?;
        Ok(Keystore {
            version: VERSION,
            key_type: self.key_type,
            crypto: Some(encrypt_bytes(&private, passphrase)?),
            private_key: String::new(),
            public_key: self.public_key.clone(),
            holder_id: self.holder_id.clone(),
            hint: hint.to_string(),
        })
    }

    /// The unencrypted form of an encrypted keystore.
    pub fn decrypt_keystore(&self, passphrase: &str) -> Result<Keystore, Error> {
        let Some(c) = &self.crypto else {
            return err("keystore is not encrypted");
        };
        let private = decrypt_crypto(c, passphrase)?;
        Ok(Keystore {
            version: VERSION,
            key_type: self.key_type,
            crypto: None,
            private_key: hex::encode(private),
            public_key: self.public_key.clone(),
            holder_id: self.holder_id.clone(),
            hint: String::new(),
        })
    }

    /// The raw private key, decrypted when needed (the passphrase is ignored
    /// for an unencrypted keystore). For an Ed25519 key the stored public key
    /// must be the one the private key derives.
    pub fn get_private_key(&self, passphrase: &str) -> Result<Vec<u8>, Error> {
        let private = match &self.crypto {
            Some(c) => decrypt_crypto(c, passphrase)?,
            None => hex::decode(&self.private_key).map_err(|e| Error(format!("invalid private key hex: {e}")))?,
        };
        if self.key_type == KEY_TYPE_ED25519 {
            verify_ed25519(&private, &self.public_key)?;
        }
        Ok(private)
    }

    pub fn is_encrypted(&self) -> bool {
        self.crypto.is_some()
    }

    /// The passphrase file: a file in the current directory named exactly as
    /// the holder ID, its content trimmed. None when there is no such file.
    pub fn read_passphrase_file(&self) -> Option<String> {
        if self.holder_id.is_empty() {
            return None;
        }
        std::fs::read_to_string(&self.holder_id).ok().map(|s| s.trim().to_string())
    }

    /// The raw private key of an encrypted keystore, without the key check.
    pub fn decrypt(&self, passphrase: &str) -> Result<Vec<u8>, Error> {
        match &self.crypto {
            Some(c) => decrypt_crypto(c, passphrase),
            None => err("keystore is not encrypted; use get_private_key instead"),
        }
    }

    /// Decrypts and runs the key-type check, returning only whether it passed.
    pub fn verify(&self, passphrase: &str) -> Result<(), Error> {
        self.get_private_key(passphrase).map(|_| ())
    }

    /// Writes the keystore as indented JSON with mode 0600.
    pub fn save_to_file(&self, path: &Path) -> Result<(), Error> {
        let data = serde_json::to_string_pretty(self).map_err(|e| Error(format!("failed to marshal keystore: {e}")))?;
        write_private(path, data.as_bytes()).map_err(|e| Error(format!("cannot write {}: {e}", path.display())))
    }

    /// Reads and structurally validates a keystore file.
    pub fn load_from_file(path: &Path) -> Result<Keystore, Error> {
        let data = std::fs::read_to_string(path).map_err(|e| Error(format!("can't read keystore file '{}': {e}", path.display())))?;
        let ks: Keystore = serde_json::from_str(&data).map_err(|e| Error(format!("can't parse keystore file '{}': {e}", path.display())))?;
        if ks.version == 0 {
            return err(format!("invalid keystore file '{}': missing version field", path.display()));
        }
        ks.validate().map_err(|e| Error(format!("invalid keystore file '{}': {e}", path.display())))?;
        Ok(ks)
    }

    /// The decoded public key.
    pub fn public_key_bytes(&self) -> Result<Vec<u8>, Error> {
        if self.public_key.is_empty() {
            return err("keystore has no public key");
        }
        hex::decode(&self.public_key).map_err(|e| Error(format!("invalid public key hex: {e}")))
    }

    fn validate(&self) -> Result<(), Error> {
        if !self.is_encrypted() && self.private_key.is_empty() {
            return err("unencrypted keystore missing private_key");
        }
        if self.public_key.is_empty() {
            return err("missing public_key");
        }
        Ok(())
    }
}

/// Whether the file looks like a JSON keystore (has a positive version).
pub fn is_keystore_file(path: &Path) -> bool {
    #[derive(Deserialize)]
    struct Probe {
        #[serde(default)]
        version: u32,
    }
    std::fs::read_to_string(path)
        .ok()
        .and_then(|d| serde_json::from_str::<Probe>(&d).ok())
        .map_or(false, |p| p.version > 0)
}

pub fn key_type_name(key_type: u32) -> String {
    match key_type {
        KEY_TYPE_ED25519 => "ED25519".to_string(),
        other => format!("unknown({other})"),
    }
}

fn encrypt_bytes(plaintext: &[u8], passphrase: &str) -> Result<CryptoData, Error> {
    let mut salt = [0u8; SALT_SIZE];
    let mut nonce = [0u8; NONCE_SIZE];
    rand::rngs::OsRng.fill_bytes(&mut salt);
    rand::rngs::OsRng.fill_bytes(&mut nonce);
    let key = derive_key(passphrase, &salt, ARGON_TIME, ARGON_MEMORY, ARGON_THREADS)?;
    let ciphertext = Aes256Gcm::new(Key::<Aes256Gcm>::from_slice(&key))
        .encrypt(Nonce::from_slice(&nonce), plaintext)
        .map_err(|_| Error("encryption failed".into()))?;
    Ok(CryptoData {
        cipher: "aes-256-gcm".into(),
        kdf: "argon2id".into(),
        kdf_params: KdfParams { time: ARGON_TIME, memory: ARGON_MEMORY, threads: ARGON_THREADS, salt: hex::encode(salt) },
        nonce: hex::encode(nonce),
        ciphertext: hex::encode(ciphertext),
    })
}

fn decrypt_crypto(c: &CryptoData, passphrase: &str) -> Result<Vec<u8>, Error> {
    if c.cipher != "aes-256-gcm" || c.kdf != "argon2id" {
        return err(format!("unsupported cipher/kdf {}/{}", c.cipher, c.kdf));
    }
    let salt = hex::decode(&c.kdf_params.salt).map_err(|e| Error(format!("invalid salt hex: {e}")))?;
    let nonce = hex::decode(&c.nonce).map_err(|e| Error(format!("invalid nonce hex: {e}")))?;
    let ciphertext = hex::decode(&c.ciphertext).map_err(|e| Error(format!("invalid ciphertext hex: {e}")))?;
    let key = derive_key(passphrase, &salt, c.kdf_params.time, c.kdf_params.memory, c.kdf_params.threads)?;
    Aes256Gcm::new(Key::<Aes256Gcm>::from_slice(&key))
        .decrypt(Nonce::from_slice(&nonce), ciphertext.as_ref())
        .map_err(|_| Error("wrong passphrase or corrupted keystore".into()))
}

/// Argon2id, version 0x13, as Go's x/crypto/argon2.IDKey computes it.
fn derive_key(passphrase: &str, salt: &[u8], time: u32, memory: u32, threads: u32) -> Result<[u8; KEY_SIZE], Error> {
    let params = Params::new(memory, time, threads, Some(KEY_SIZE)).map_err(|e| Error(format!("argon2 parameters: {e}")))?;
    let mut key = [0u8; KEY_SIZE];
    Argon2::new(Algorithm::Argon2id, Version::V0x13, params)
        .hash_password_into(passphrase.as_bytes(), salt, &mut key)
        .map_err(|e| Error(format!("argon2: {e}")))?;
    Ok(key)
}

/// The private key must be 64 bytes and derive the stored public key, as
/// Go's crypto/ed25519 derives it from the seed in its first half.
fn verify_ed25519(private: &[u8], stored_pub_hex: &str) -> Result<(), Error> {
    if private.len() != ED25519_PRIVATE_KEY_SIZE {
        return err(format!("key has wrong size for ED25519: {} (expected {ED25519_PRIVATE_KEY_SIZE})", private.len()));
    }
    let stored = hex::decode(stored_pub_hex).map_err(|e| Error(format!("invalid stored public key hex: {e}")))?;
    let h = Sha512::digest(&private[..32]);
    let mut lo = [0u8; 32];
    lo.copy_from_slice(&h[..32]);
    lo[0] &= 248;
    lo[31] &= 127;
    lo[31] |= 64;
    let x = curve25519_dalek::scalar::Scalar::from_bytes_mod_order(lo);
    let derived = curve25519_dalek::edwards::EdwardsPoint::mul_base(&x).compress();
    if derived.as_bytes()[..] != stored[..] {
        return err("private key does not match stored public key (keystore corrupted)");
    }
    Ok(())
}

#[cfg(unix)]
fn write_private(path: &Path, data: &[u8]) -> std::io::Result<()> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = std::fs::OpenOptions::new().write(true).create(true).truncate(true).mode(0o600).open(path)?;
    f.write_all(data)
}

#[cfg(not(unix))]
fn write_private(path: &Path, data: &[u8]) -> std::io::Result<()> {
    std::fs::write(path, data)
}

#[cfg(test)]
mod tests {
    use super::*;

    // Written by the Go side (util/keystore.Encrypt) for the seed below with
    // the passphrase below: the fixed point for the Argon2id parameters and
    // the GCM layout.
    const GO_ENCRYPTED: &str = r#"{"version":1,"key_type":0,"crypto":{"cipher":"aes-256-gcm","kdf":"argon2id","kdf_params":{"time":3,"memory":65536,"threads":4,"salt":"849c2e3f7bffbbadbbe3aac84805747f"},"nonce":"4e2c51fb6fdb5992d41e92de","ciphertext":"56e5f87c880516fa109606603e818c5c703c0427e3c5935e04e8d121b9744ec3b3e725eadd13f37f1c79839aa143684ab77b1c081cde2f1804696d9ae34016432e9d25b70dafd969432ac07d8419e9b4"},"public_key":"d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a","holder_id":"0123456789abcdef"}"#;
    const PASSPHRASE: &str = "correct horse battery staple";
    const SEED: &str = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60";
    const PUBKEY: &str = "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a";

    #[test]
    fn reads_a_go_written_keystore() {
        let ks: Keystore = serde_json::from_str(GO_ENCRYPTED).unwrap();
        assert!(ks.is_encrypted());
        let private = ks.get_private_key(PASSPHRASE).unwrap();
        assert_eq!(hex::encode(&private[..32]), SEED);
        assert_eq!(hex::encode(&private[32..]), PUBKEY);
        assert!(ks.get_private_key("not the passphrase").is_err());
        assert!(ks.verify(PASSPHRASE).is_ok());
    }

    #[test]
    fn round_trips() {
        let private = hex::decode(format!("{SEED}{PUBKEY}")).unwrap();
        let pubkey = hex::decode(PUBKEY).unwrap();
        let plain = Keystore::new_unencrypted(KEY_TYPE_ED25519, &private, &pubkey, "holder").unwrap();
        assert_eq!(plain.get_private_key("").unwrap(), private);
        let enc = plain.encrypt_keystore("a passphrase", "the hint").unwrap();
        assert!(enc.is_encrypted() && enc.private_key.is_empty() && enc.hint == "the hint");
        assert_eq!(enc.get_private_key("a passphrase").unwrap(), private);
        assert!(enc.get_private_key("another").is_err());
        let back = enc.decrypt_keystore("a passphrase").unwrap();
        assert_eq!(back.private_key, plain.private_key);

        // a corrupted public key is caught by the Ed25519 check
        let mut bad = plain.clone();
        bad.public_key = hex::encode([7u8; 32]);
        assert!(bad.get_private_key("").is_err());

        // JSON field presence follows the Go struct
        let j = serde_json::to_value(&enc).unwrap();
        assert!(j.get("private_key").is_none() && j.get("crypto").is_some() && j.get("holder_id").is_some());
        let j = serde_json::to_value(&plain).unwrap();
        assert!(j.get("crypto").is_none() && j.get("hint").is_none());
    }
}
