// Package keystore implements passphrase-encrypted key storage using Argon2id KDF and AES-256-GCM.
// The keystore format supports multiple key types (ED25519, future BLS) and stores the public key
// in plaintext for identification and post-decryption verification.
package keystore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/crypto/argon2"
)

const (
	KeyTypeED25519 = 0
	// KeyTypeBLSPartial = 1 // future: partial BLS key for threshold multisig

	Version = 1

	defaultArgonTime    = 3
	defaultArgonMemory  = 64 * 1024 // 64 MiB
	defaultArgonThreads = 4
	saltSize            = 16
	nonceSize           = 12 // AES-GCM standard nonce size
	keySize             = 32 // AES-256
)

type KDFParams struct {
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"`
	Threads uint8  `json:"threads"`
	Salt    string `json:"salt"` // hex-encoded
}

type CryptoData struct {
	Cipher     string    `json:"cipher"`
	KDF        string    `json:"kdf"`
	KDFParams  KDFParams `json:"kdf_params"`
	Nonce      string    `json:"nonce"`      // hex-encoded
	Ciphertext string    `json:"ciphertext"` // hex-encoded, includes GCM auth tag
}

type Keystore struct {
	Version int        `json:"version"`
	KeyType int        `json:"key_type"`
	Crypto  CryptoData `json:"crypto"`
	PubKey  string     `json:"pubkey"` // hex-encoded, for identification and post-decryption verification
}

// Encrypt creates a new keystore by encrypting privateKey with the given passphrase.
// pubkey is stored in plaintext for identification and verification after decryption.
func Encrypt(keyType int, privateKey, pubkey []byte, passphrase string) (*Keystore, error) {
	if len(passphrase) == 0 {
		return nil, fmt.Errorf("passphrase must not be empty")
	}

	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("failed to generate salt: %v", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %v", err)
	}

	// Derive encryption key from passphrase via Argon2id
	derivedKey := argon2.IDKey([]byte(passphrase), salt, defaultArgonTime, defaultArgonMemory, defaultArgonThreads, keySize)

	// Encrypt with AES-256-GCM
	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %v", err)
	}
	ciphertext := gcm.Seal(nil, nonce, privateKey, nil)

	return &Keystore{
		Version: Version,
		KeyType: keyType,
		Crypto: CryptoData{
			Cipher: "aes-256-gcm",
			KDF:    "argon2id",
			KDFParams: KDFParams{
				Time:    defaultArgonTime,
				Memory:  defaultArgonMemory,
				Threads: defaultArgonThreads,
				Salt:    hex.EncodeToString(salt),
			},
			Nonce:      hex.EncodeToString(nonce),
			Ciphertext: hex.EncodeToString(ciphertext),
		},
		PubKey: hex.EncodeToString(pubkey),
	}, nil
}

// Decrypt decrypts the keystore with the given passphrase and returns the raw private key bytes.
// GCM authentication tag verifies the passphrase is correct.
func (ks *Keystore) Decrypt(passphrase string) ([]byte, error) {
	salt, err := hex.DecodeString(ks.Crypto.KDFParams.Salt)
	if err != nil {
		return nil, fmt.Errorf("invalid salt hex: %v", err)
	}
	nonce, err := hex.DecodeString(ks.Crypto.Nonce)
	if err != nil {
		return nil, fmt.Errorf("invalid nonce hex: %v", err)
	}
	ciphertext, err := hex.DecodeString(ks.Crypto.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("invalid ciphertext hex: %v", err)
	}

	params := ks.Crypto.KDFParams
	derivedKey := argon2.IDKey([]byte(passphrase), salt, params.Time, params.Memory, params.Threads, keySize)

	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %v", err)
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("wrong passphrase or corrupted keystore")
	}
	return plaintext, nil
}

// Verify decrypts the keystore and performs key-type-specific public key verification.
// For ED25519: derives public key from decrypted private key and compares to stored pubkey.
func (ks *Keystore) Verify(passphrase string) error {
	privateKeyBytes, err := ks.Decrypt(passphrase)
	if err != nil {
		return err
	}

	storedPubkey, err := hex.DecodeString(ks.PubKey)
	if err != nil {
		return fmt.Errorf("invalid stored pubkey hex: %v", err)
	}

	switch ks.KeyType {
	case KeyTypeED25519:
		if len(privateKeyBytes) != ed25519.PrivateKeySize {
			return fmt.Errorf("decrypted key has wrong size for ED25519: %d (expected %d)", len(privateKeyBytes), ed25519.PrivateKeySize)
		}
		derivedPubkey := ed25519.PrivateKey(privateKeyBytes).Public().(ed25519.PublicKey)
		if !equal(derivedPubkey, storedPubkey) {
			return fmt.Errorf("decrypted key does not match stored public key (keystore corrupted)")
		}
	default:
		// For unknown key types, decryption succeeded (GCM auth passed) but we can't verify the pubkey
		return fmt.Errorf("key type %d: decryption OK, pubkey verification not available", ks.KeyType)
	}
	return nil
}

// SaveToFile marshals the keystore as JSON and writes it with 0600 permissions.
func (ks *Keystore) SaveToFile(path string) error {
	data, err := json.MarshalIndent(ks, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal keystore: %v", err)
	}
	return os.WriteFile(path, data, 0600)
}

// LoadFromFile reads and unmarshals a keystore from a JSON file.
func LoadFromFile(path string) (*Keystore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("can't read keystore file '%s': %v", path, err)
	}
	var ks Keystore
	if err := json.Unmarshal(data, &ks); err != nil {
		return nil, fmt.Errorf("can't parse keystore file '%s': %v", path, err)
	}
	if ks.Version == 0 {
		return nil, fmt.Errorf("invalid keystore file '%s': missing version field", path)
	}
	return &ks, nil
}

// IsKeystoreFile returns true if the file at path appears to be a JSON keystore
// (as opposed to a plain hex key file).
func IsKeystoreFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.Version > 0
}

// KeyTypeName returns a human-readable name for the key type.
func KeyTypeName(keyType int) string {
	switch keyType {
	case KeyTypeED25519:
		return "ED25519"
	default:
		return fmt.Sprintf("unknown(%d)", keyType)
	}
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
