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

// TestEncryptDecryptRoundTrip verifies that encrypting and decrypting a key produces the original.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	passphrase := "test-passphrase-123"

	ks, err := Encrypt(KeyTypeED25519, priv, pub, passphrase)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Verify keystore fields
	if ks.Version != Version {
		t.Errorf("expected version %d, got %d", Version, ks.Version)
	}
	if ks.KeyType != KeyTypeED25519 {
		t.Errorf("expected key type %d, got %d", KeyTypeED25519, ks.KeyType)
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

	ks, err := Encrypt(KeyTypeED25519, priv, pub, "correct-passphrase")
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

	ks, err := Encrypt(KeyTypeED25519, priv, pub, "passphrase")
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

	ks, err := Encrypt(KeyTypeED25519, priv, pub, "correct")
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
	pub := priv.Public().(ed25519.PublicKey)

	ks, err := Encrypt(KeyTypeED25519, priv, pub, "passphrase")
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Replace stored pubkey with a different key's pubkey
	otherPriv := generateTestKey(t)
	otherPub := otherPriv.Public().(ed25519.PublicKey)
	ks.PubKey = string(otherPub) // corrupt the pubkey field

	// Re-encode properly with hex
	_, err = Encrypt(KeyTypeED25519, priv, otherPub, "passphrase")
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Encrypt with mismatched pubkey stored
	ks2, _ := Encrypt(KeyTypeED25519, priv, otherPub, "passphrase")
	err = ks2.Verify("passphrase")
	if err == nil {
		t.Fatal("expected error for pubkey mismatch")
	}
	t.Logf("expected error: %v", err)
}

// TestSaveLoadRoundTrip verifies file I/O preserves the keystore.
func TestSaveLoadRoundTrip(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	passphrase := "file-test-passphrase"

	ks, err := Encrypt(KeyTypeED25519, priv, pub, passphrase)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	path := filepath.Join(t.TempDir(), "test.keystore")
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

	// Verify loaded keystore decrypts correctly
	decrypted, err := loaded.Decrypt(passphrase)
	if err != nil {
		t.Fatalf("Decrypt on loaded keystore failed: %v", err)
	}
	if !equal(decrypted, priv) {
		t.Fatal("decrypted key from loaded keystore does not match original")
	}
}

// TestIsKeystoreFile verifies detection of keystore vs plain hex files.
func TestIsKeystoreFile(t *testing.T) {
	dir := t.TempDir()

	// Write a valid keystore file
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	ks, _ := Encrypt(KeyTypeED25519, priv, pub, "pass")
	ksPath := filepath.Join(dir, "test.keystore")
	if err := ks.SaveToFile(ksPath); err != nil {
		t.Fatalf("SaveToFile failed: %v", err)
	}

	// Write a plain hex key file
	hexPath := filepath.Join(dir, "test.key")
	if err := os.WriteFile(hexPath, []byte("abcdef0123456789\n"), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if !IsKeystoreFile(ksPath) {
		t.Error("expected IsKeystoreFile to return true for keystore file")
	}
	if IsKeystoreFile(hexPath) {
		t.Error("expected IsKeystoreFile to return false for plain hex file")
	}
	if IsKeystoreFile(filepath.Join(dir, "nonexistent")) {
		t.Error("expected IsKeystoreFile to return false for nonexistent file")
	}
}

// TestEmptyPassphrase verifies that empty passphrase is rejected.
func TestEmptyPassphrase(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	_, err := Encrypt(KeyTypeED25519, priv, pub, "")
	if err == nil {
		t.Fatal("expected error for empty passphrase")
	}
}

// TestUnknownKeyTypeVerify verifies that Verify reports unsupported key type.
func TestUnknownKeyTypeVerify(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	ks, err := Encrypt(99, priv, pub, "passphrase")
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	err = ks.Verify("passphrase")
	if err == nil {
		t.Fatal("expected error for unknown key type verification")
	}
	t.Logf("expected error: %v", err)
}
