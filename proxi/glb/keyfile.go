package glb

import (
	"crypto/ed25519"
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/util/keystore"
	"golang.org/x/term"
)

// LoadPrivateKeyFromFile loads an ED25519 private key from a .key file (JSON keystore format).
// If the keystore is encrypted, checks PROXIMA_KEY_PASSPHRASE env var, then prompts on stdin.
func LoadPrivateKeyFromFile(path string) (ed25519.PrivateKey, error) {
	key, passphrase, err := loadPrivateKey(path, true)
	if err != nil {
		return nil, err
	}
	keyPassphrase = passphrase
	return key, nil
}

// LoadColdPrivateKeyFromFile loads the cold wallet's key. PROXIMA_KEY_PASSPHRASE is the hot
// key's passphrase and is never tried, and the hot passphrase handed to a nonce seeker is left
// as it is.
func LoadColdPrivateKeyFromFile(path string) (ed25519.PrivateKey, error) {
	key, _, err := loadPrivateKey(path, false)
	return key, err
}

// loadPrivateKey unlocks an encrypted keystore with its passphrase file, then, if useEnv, with
// PROXIMA_KEY_PASSPHRASE, then by prompting. Returns the passphrase it used.
func loadPrivateKey(path string, useEnv bool) (ed25519.PrivateKey, string, error) {
	ks, err := keystore.LoadFromFile(path)
	if err != nil {
		return nil, "", err
	}
	if ks.KeyType != keystore.KeyTypeED25519 {
		return nil, "", fmt.Errorf("unsupported key type %d in '%s'", ks.KeyType, path)
	}

	passphrase := ""
	if ks.IsEncrypted() {
		if p, ok := ks.ReadPassphraseFile(); ok {
			passphrase = p
		} else if p := os.Getenv("PROXIMA_KEY_PASSPHRASE"); useEnv && p != "" {
			passphrase = p
		} else {
			hint := ""
			if ks.Hint != "" {
				hint = fmt.Sprintf(" (hint: %s)", ks.Hint)
			}
			fmt.Printf("Enter passphrase for '%s'%s: ", path, hint)
			passBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
			if err != nil {
				return nil, "", fmt.Errorf("failed to read passphrase: %v", err)
			}
			fmt.Println()
			passphrase = string(passBytes)
		}
	}

	keyBytes, err := ks.GetPrivateKey(passphrase)
	if err != nil {
		return nil, "", err
	}
	if len(keyBytes) != ed25519.PrivateKeySize {
		return nil, "", fmt.Errorf("key has wrong size: %d (expected %d)", len(keyBytes), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(keyBytes), passphrase, nil
}

// keyPassphrase is the passphrase the key file was last unlocked with, handed to
// a child process that opens the same key file, such as a spawned nonce seeker
var keyPassphrase string

// KeyPassphrase returns the passphrase the wallet key was unlocked with, empty
// for an unencrypted key.
func KeyPassphrase() string {
	return keyPassphrase
}

const minPassphraseLength = 10

// ReadPassphraseConfirm prompts for passphrase twice (no-echo) and returns it.
func ReadPassphraseConfirm() string {
	fmt.Print("Enter passphrase: ")
	pass1, err := term.ReadPassword(int(os.Stdin.Fd()))
	AssertNoError(err)
	fmt.Println()

	Assertf(len(pass1) >= minPassphraseLength, "passphrase must be at least %d characters", minPassphraseLength)

	fmt.Print("Confirm passphrase: ")
	pass2, err := term.ReadPassword(int(os.Stdin.Fd()))
	AssertNoError(err)
	fmt.Println()

	Assertf(string(pass1) == string(pass2), "passphrases do not match")
	return string(pass1)
}
