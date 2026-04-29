package keystore

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

// generateTestKey creates a random ED25519 key pair for testing.
func generateTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate test key: %v", err)
	}
	return priv
}

const testHolderID = "a(0xtest_holder_id)"

// TestNewUnencrypted verifies creation and field correctness of unencrypted keystores.
func TestNewUnencrypted(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, err := NewUnencrypted(KeyTypeED25519, priv, pub, testHolderID)
	if err != nil {
		t.Fatalf("NewUnencrypted failed: %v", err)
	}

	if ks.Version != Version {
		t.Errorf("expected version %d, got %d", Version, ks.Version)
	}
	if ks.IsEncrypted() {
		t.Error("expected IsEncrypted()=false")
	}
	if ks.Crypto != nil {
		t.Error("expected Crypto=nil for unencrypted")
	}
	if ks.PrivateKey == "" {
		t.Error("expected PrivateKey to be set")
	}
	if ks.PublicKey == "" {
		t.Error("expected PublicKey to be set")
	}
	if ks.HolderID != testHolderID {
		t.Errorf("expected HolderID=%q, got %q", testHolderID, ks.HolderID)
	}
}

// TestNewUnencryptedGetPrivateKey verifies GetPrivateKey works without passphrase on unencrypted keystores.
func TestNewUnencryptedGetPrivateKey(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, err := NewUnencrypted(KeyTypeED25519, priv, pub, testHolderID)
	if err != nil {
		t.Fatalf("NewUnencrypted failed: %v", err)
	}

	// Passphrase is ignored for unencrypted
	got, err := ks.GetPrivateKey("")
	if err != nil {
		t.Fatalf("GetPrivateKey failed: %v", err)
	}
	if !equal(got, priv) {
		t.Fatal("GetPrivateKey returned wrong key")
	}
}

// TestEncryptDecryptRoundTrip verifies that encrypting and decrypting a key produces the original.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	passphrase := "test-passphrase-123"

	ks, err := Encrypt(KeyTypeED25519, priv, pub, passphrase, testHolderID)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	if ks.Version != Version {
		t.Errorf("expected version %d, got %d", Version, ks.Version)
	}
	if !ks.IsEncrypted() {
		t.Error("expected IsEncrypted()=true")
	}
	if ks.Crypto == nil {
		t.Error("expected Crypto to be set")
	}
	if ks.PrivateKey != "" {
		t.Error("expected PrivateKey to be empty for encrypted keystore")
	}

	decrypted, err := ks.Decrypt(passphrase)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if !equal(decrypted, priv) {
		t.Fatal("decrypted key does not match original")
	}
}

// TestWrongPassphrase verifies that GCM authentication rejects wrong passphrase.
func TestWrongPassphrase(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, err := Encrypt(KeyTypeED25519, priv, pub, "correct-passphrase", testHolderID)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	_, err = ks.Decrypt("wrong-passphrase")
	if err == nil {
		t.Fatal("expected error for wrong passphrase, got nil")
	}
	t.Logf("expected error: %v", err)
}

// TestVerifySuccess verifies that Verify succeeds with correct passphrase and matching pubkey.
func TestVerifySuccess(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, err := Encrypt(KeyTypeED25519, priv, pub, "passphrase", testHolderID)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	if err := ks.Verify("passphrase"); err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
}

// TestVerifyWrongPassphrase verifies that Verify fails with wrong passphrase.
func TestVerifyWrongPassphrase(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, err := Encrypt(KeyTypeED25519, priv, pub, "correct", testHolderID)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	if err := ks.Verify("wrong"); err == nil {
		t.Fatal("expected error for wrong passphrase")
	}
}

// TestVerifyPubkeyMismatch verifies that Verify detects a corrupted pubkey field.
func TestVerifyPubkeyMismatch(t *testing.T) {
	priv := generateTestKey(t)
	otherPriv := generateTestKey(t)
	otherPub := otherPriv.Public().(ed25519.PublicKey)

	// Encrypt with one key but store a different public key
	ks, err := Encrypt(KeyTypeED25519, priv, otherPub, "passphrase", testHolderID)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	err = ks.Verify("passphrase")
	if err == nil {
		t.Fatal("expected error for pubkey mismatch")
	}
	t.Logf("expected error: %v", err)
}

// TestSaveLoadRoundTrip verifies file I/O preserves the keystore (encrypted).
func TestSaveLoadRoundTrip(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	passphrase := "file-test-passphrase"

	ks, err := Encrypt(KeyTypeED25519, priv, pub, passphrase, testHolderID)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	path := filepath.Join(t.TempDir(), "test.key")
	if err := ks.SaveToFile(path); err != nil {
		t.Fatalf("SaveToFile failed: %v", err)
	}

	// Check file permissions
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected permissions 0600, got %04o", perm)
	}

	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	decrypted, err := loaded.GetPrivateKey(passphrase)
	if err != nil {
		t.Fatalf("GetPrivateKey on loaded keystore failed: %v", err)
	}
	if !equal(decrypted, priv) {
		t.Fatal("decrypted key from loaded keystore does not match original")
	}
	if loaded.HolderID != testHolderID {
		t.Errorf("expected HolderID=%q, got %q", testHolderID, loaded.HolderID)
	}
}

// TestSaveLoadUnencrypted verifies file I/O for unencrypted keystores.
func TestSaveLoadUnencrypted(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, err := NewUnencrypted(KeyTypeED25519, priv, pub, testHolderID)
	if err != nil {
		t.Fatalf("NewUnencrypted failed: %v", err)
	}

	path := filepath.Join(t.TempDir(), "test.key")
	if err := ks.SaveToFile(path); err != nil {
		t.Fatalf("SaveToFile failed: %v", err)
	}

	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if loaded.IsEncrypted() {
		t.Error("loaded keystore should not be encrypted")
	}

	got, err := loaded.GetPrivateKey("")
	if err != nil {
		t.Fatalf("GetPrivateKey failed: %v", err)
	}
	if !equal(got, priv) {
		t.Fatal("loaded key does not match original")
	}
}

// TestEncryptKeystore verifies encrypting an unencrypted keystore.
func TestEncryptKeystore(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, err := NewUnencrypted(KeyTypeED25519, priv, pub, testHolderID)
	if err != nil {
		t.Fatalf("NewUnencrypted failed: %v", err)
	}

	encrypted, err := EncryptKeystore(ks, "my-passphrase", "test hint")
	if err != nil {
		t.Fatalf("EncryptKeystore failed: %v", err)
	}

	if !encrypted.IsEncrypted() {
		t.Error("expected encrypted keystore")
	}
	if encrypted.Hint != "test hint" {
		t.Errorf("expected hint %q, got %q", "test hint", encrypted.Hint)
	}
	if encrypted.PrivateKey != "" {
		t.Error("encrypted keystore should not have plaintext private key")
	}

	got, err := encrypted.GetPrivateKey("my-passphrase")
	if err != nil {
		t.Fatalf("GetPrivateKey failed: %v", err)
	}
	if !equal(got, priv) {
		t.Fatal("decrypted key does not match original")
	}
}

// TestEncryptKeystoreAlreadyEncrypted verifies error when encrypting an already-encrypted keystore.
func TestEncryptKeystoreAlreadyEncrypted(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, err := Encrypt(KeyTypeED25519, priv, pub, "pass", testHolderID)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	_, err = EncryptKeystore(ks, "pass2", "")
	if err == nil {
		t.Fatal("expected error when encrypting already-encrypted keystore")
	}
}

// TestDecryptKeystore verifies decrypting an encrypted keystore to unencrypted.
func TestDecryptKeystore(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	passphrase := "decrypt-test"

	encrypted, err := Encrypt(KeyTypeED25519, priv, pub, passphrase, testHolderID)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	decrypted, err := DecryptKeystore(encrypted, passphrase)
	if err != nil {
		t.Fatalf("DecryptKeystore failed: %v", err)
	}

	if decrypted.IsEncrypted() {
		t.Error("expected unencrypted keystore")
	}
	if decrypted.Crypto != nil {
		t.Error("expected nil Crypto")
	}
	if decrypted.HolderID != testHolderID {
		t.Errorf("expected HolderID=%q, got %q", testHolderID, decrypted.HolderID)
	}

	got, err := decrypted.GetPrivateKey("")
	if err != nil {
		t.Fatalf("GetPrivateKey failed: %v", err)
	}
	if !equal(got, priv) {
		t.Fatal("decrypted key does not match original")
	}
}

// TestDecryptKeystoreNotEncrypted verifies error when decrypting an unencrypted keystore.
func TestDecryptKeystoreNotEncrypted(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, _ := NewUnencrypted(KeyTypeED25519, priv, pub, testHolderID)
	_, err := DecryptKeystore(ks, "pass")
	if err == nil {
		t.Fatal("expected error when decrypting unencrypted keystore")
	}
}

// TestIsKeystoreFile verifies detection of keystore vs non-keystore files.
func TestIsKeystoreFile(t *testing.T) {
	dir := t.TempDir()

	// Write a valid v2 keystore file
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	ks, _ := NewUnencrypted(KeyTypeED25519, priv, pub, testHolderID)
	ksPath := filepath.Join(dir, "test.key")
	if err := ks.SaveToFile(ksPath); err != nil {
		t.Fatalf("SaveToFile failed: %v", err)
	}

	// Write a plain text file
	hexPath := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(hexPath, []byte("abcdef0123456789\n"), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if !IsKeystoreFile(ksPath) {
		t.Error("expected IsKeystoreFile=true for keystore")
	}
	if IsKeystoreFile(hexPath) {
		t.Error("expected IsKeystoreFile=false for plain text")
	}
	if IsKeystoreFile(filepath.Join(dir, "nonexistent")) {
		t.Error("expected IsKeystoreFile=false for nonexistent file")
	}
}

// TestEmptyPassphrase verifies that empty passphrase is rejected for encryption.
func TestEmptyPassphrase(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	_, err := Encrypt(KeyTypeED25519, priv, pub, "", testHolderID)
	if err == nil {
		t.Fatal("expected error for empty passphrase")
	}
}

// TestUnknownKeyTypeVerify verifies that GetPrivateKey skips pubkey verification for unknown types.
func TestUnknownKeyTypeVerify(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, err := Encrypt(99, priv, pub, "passphrase", testHolderID)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// GetPrivateKey should succeed for unknown key types (no pubkey verification)
	got, err := ks.GetPrivateKey("passphrase")
	if err != nil {
		t.Fatalf("GetPrivateKey failed: %v", err)
	}
	if !equal(got, priv) {
		t.Fatal("key mismatch")
	}
}

// TestDecryptOnUnencryptedErrors verifies that Decrypt returns an error on unencrypted keystores.
func TestDecryptOnUnencryptedErrors(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, _ := NewUnencrypted(KeyTypeED25519, priv, pub, testHolderID)
	_, err := ks.Decrypt("anything")
	if err == nil {
		t.Fatal("expected error when calling Decrypt on unencrypted keystore")
	}
}

// TestHintPreserved verifies that the hint field survives save/load round-trip.
func TestHintPreserved(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, err := NewUnencrypted(KeyTypeED25519, priv, pub, testHolderID)
	if err != nil {
		t.Fatalf("NewUnencrypted failed: %v", err)
	}

	encrypted, err := EncryptKeystore(ks, "pass", "my secret hint")
	if err != nil {
		t.Fatalf("EncryptKeystore failed: %v", err)
	}

	path := filepath.Join(t.TempDir(), "hint.key")
	if err := encrypted.SaveToFile(path); err != nil {
		t.Fatalf("SaveToFile failed: %v", err)
	}

	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}
	if loaded.Hint != "my secret hint" {
		t.Errorf("expected Hint=%q, got %q", "my secret hint", loaded.Hint)
	}
}
