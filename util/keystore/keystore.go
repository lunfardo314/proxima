// Package keystore implements key storage using a unified JSON format.
// Supports both encrypted (Argon2id KDF + AES-256-GCM) and unencrypted keys.
// The public key and sender ID are stored in plaintext for identification.
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

	DefaultKeyFile = "proxima.key"

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

// Keystore is the unified key file format.
// Encrypted keystores have Crypto populated and PrivateKey empty.
// Unencrypted keystores have PrivateKey populated and Crypto nil.
type Keystore struct {
	Version    int         `json:"version"`
	KeyType    int         `json:"key_type"`
	Crypto     *CryptoData `json:"crypto,omitempty"`      // non-nil means encrypted
	PrivateKey string      `json:"private_key,omitempty"` // hex-encoded, present when not encrypted
	PublicKey  string      `json:"public_key"`
	SpenderID   string      `json:"spender_id"`
	Hint       string      `json:"hint,omitempty"` // optional passphrase hint for encrypted keystores
}

// NewUnencrypted creates an unencrypted keystore.
// senderID is an opaque string (e.g. the Proxima account address) stored for identification.
func NewUnencrypted(keyType int, privateKey, pubkey []byte, senderID string) (*Keystore, error) {
	if len(privateKey) == 0 {
		return nil, fmt.Errorf("private key must not be empty")
	}
	if len(pubkey) == 0 {
		return nil, fmt.Errorf("public key must not be empty")
	}
	return &Keystore{
		Version:    Version,
		KeyType:    keyType,
		PrivateKey: hex.EncodeToString(privateKey),
		PublicKey:  hex.EncodeToString(pubkey),
		SpenderID:  senderID,
	}, nil
}

// Encrypt creates a new encrypted keystore by encrypting privateKey with the given passphrase.
// pubkey and senderID are stored in plaintext for identification.
func Encrypt(keyType int, privateKey, pubkey []byte, passphrase, senderID string) (*Keystore, error) {
	if len(passphrase) == 0 {
		return nil, fmt.Errorf("passphrase must not be empty")
	}

	cryptoData, err := encryptBytes(privateKey, passphrase)
	if err != nil {
		return nil, err
	}

	return &Keystore{
		Version:   Version,
		KeyType:   keyType,
		Crypto:    cryptoData,
		PublicKey: hex.EncodeToString(pubkey),
		SpenderID: senderID,
	}, nil
}

// EncryptKeystore encrypts an existing unencrypted keystore, returning a new encrypted one.
func EncryptKeystore(ks *Keystore, passphrase, hint string) (*Keystore, error) {
	if ks.IsEncrypted() {
		return nil, fmt.Errorf("keystore is already encrypted")
	}
	if len(passphrase) == 0 {
		return nil, fmt.Errorf("passphrase must not be empty")
	}

	privBytes, err := hex.DecodeString(ks.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("invalid private key hex: %v", err)
	}

	cryptoData, err := encryptBytes(privBytes, passphrase)
	if err != nil {
		return nil, err
	}

	return &Keystore{
		Version:   Version,
		KeyType:   ks.KeyType,
		Crypto:    cryptoData,
		PublicKey: ks.PublicKey,
		SpenderID: ks.SpenderID,
		Hint:      hint,
	}, nil
}

// DecryptKeystore decrypts an encrypted keystore, returning a new unencrypted one.
func DecryptKeystore(ks *Keystore, passphrase string) (*Keystore, error) {
	if !ks.IsEncrypted() {
		return nil, fmt.Errorf("keystore is not encrypted")
	}

	privBytes, err := decryptCrypto(ks.Crypto, passphrase)
	if err != nil {
		return nil, err
	}

	return &Keystore{
		Version:    Version,
		KeyType:    ks.KeyType,
		PrivateKey: hex.EncodeToString(privBytes),
		PublicKey:  ks.PublicKey,
		SpenderID:  ks.SpenderID,
	}, nil
}

// GetPrivateKey returns the raw private key bytes regardless of encryption state.
// For encrypted keystores, passphrase is required. For unencrypted, passphrase is ignored.
// Performs public key verification for ED25519 keys.
func (ks *Keystore) GetPrivateKey(passphrase string) ([]byte, error) {
	var privBytes []byte
	var err error

	if ks.IsEncrypted() {
		privBytes, err = decryptCrypto(ks.Crypto, passphrase)
		if err != nil {
			return nil, err
		}
	} else {
		if ks.PrivateKey == "" {
			return nil, fmt.Errorf("unencrypted keystore has no private key")
		}
		privBytes, err = hex.DecodeString(ks.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("invalid private key hex: %v", err)
		}
	}

	// Verify against stored public key for ED25519
	if ks.KeyType == KeyTypeED25519 {
		if err := verifyED25519(privBytes, ks.PublicKey); err != nil {
			return nil, err
		}
	}

	return privBytes, nil
}

// IsEncrypted returns true if the keystore is encrypted.
func (ks *Keystore) IsEncrypted() bool {
	return ks.Crypto != nil
}

// Decrypt decrypts an encrypted keystore and returns the raw private key bytes.
// Returns an error if the keystore is not encrypted.
func (ks *Keystore) Decrypt(passphrase string) ([]byte, error) {
	if !ks.IsEncrypted() {
		return nil, fmt.Errorf("keystore is not encrypted; use GetPrivateKey instead")
	}
	return decryptCrypto(ks.Crypto, passphrase)
}

// Verify decrypts the keystore and performs key-type-specific public key verification.
// For unencrypted keystores, verifies that private key matches stored public key without passphrase.
func (ks *Keystore) Verify(passphrase string) error {
	_, err := ks.GetPrivateKey(passphrase)
	return err
}

// SaveToFile marshals the keystore as JSON and writes it with 0600 permissions.
func (ks *Keystore) SaveToFile(path string) error {
	data, err := json.MarshalIndent(ks, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal keystore: %v", err)
	}
	return os.WriteFile(path, data, 0600)
}

// LoadFromFile reads a keystore from a JSON file.
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
	if err := ks.validate(); err != nil {
		return nil, fmt.Errorf("invalid keystore file '%s': %v", path, err)
	}
	return &ks, nil
}

// IsKeystoreFile returns true if the file at path appears to be a JSON keystore.
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

// PublicKeyBytes returns the decoded public key bytes from the keystore.
func PublicKeyBytes(ks *Keystore) ([]byte, error) {
	if ks.PublicKey == "" {
		return nil, fmt.Errorf("keystore has no public key")
	}
	return hex.DecodeString(ks.PublicKey)
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

// validate checks structural consistency of a keystore.
func (ks *Keystore) validate() error {
	if !ks.IsEncrypted() && ks.PrivateKey == "" {
		return fmt.Errorf("unencrypted keystore missing private_key")
	}
	if ks.PublicKey == "" {
		return fmt.Errorf("missing public_key")
	}
	return nil
}

// encryptBytes encrypts raw bytes with the given passphrase using Argon2id + AES-256-GCM.
func encryptBytes(plaintext []byte, passphrase string) (*CryptoData, error) {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("failed to generate salt: %v", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %v", err)
	}

	derivedKey := argon2.IDKey([]byte(passphrase), salt, defaultArgonTime, defaultArgonMemory, defaultArgonThreads, keySize)

	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %v", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	return &CryptoData{
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
	}, nil
}

// decryptCrypto decrypts CryptoData with the given passphrase.
func decryptCrypto(cd *CryptoData, passphrase string) ([]byte, error) {
	salt, err := hex.DecodeString(cd.KDFParams.Salt)
	if err != nil {
		return nil, fmt.Errorf("invalid salt hex: %v", err)
	}
	nonce, err := hex.DecodeString(cd.Nonce)
	if err != nil {
		return nil, fmt.Errorf("invalid nonce hex: %v", err)
	}
	ciphertext, err := hex.DecodeString(cd.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("invalid ciphertext hex: %v", err)
	}

	params := cd.KDFParams
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
		return nil, fmt.Errorf("wrong passphrase or corrupted keystore: %v", err)
	}
	return plaintext, nil
}

// verifyED25519 checks that privBytes derives the expected public key.
func verifyED25519(privBytes []byte, storedPubKeyHex string) error {
	if len(privBytes) != ed25519.PrivateKeySize {
		return fmt.Errorf("key has wrong size for ED25519: %d (expected %d)", len(privBytes), ed25519.PrivateKeySize)
	}
	storedPubkey, err := hex.DecodeString(storedPubKeyHex)
	if err != nil {
		return fmt.Errorf("invalid stored public key hex: %v", err)
	}
	derivedPubkey := ed25519.PrivateKey(privBytes).Public().(ed25519.PublicKey)
	if !equal(derivedPubkey, storedPubkey) {
		return fmt.Errorf("private key does not match stored public key (keystore corrupted)")
	}
	return nil
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
