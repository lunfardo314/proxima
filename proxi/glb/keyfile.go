package glb

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"syscall"

	"github.com/lunfardo314/proxima/util/keystore"
	"golang.org/x/term"
)

// LoadPrivateKeyFromFile loads an ED25519 private key from a .key file (JSON keystore format).
// If the keystore is encrypted, checks PROXIMA_KEY_PASSPHRASE env var, then prompts on stdin.
func LoadPrivateKeyFromFile(path string) (ed25519.PrivateKey, error) {
	ks, err := keystore.LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	if ks.KeyType != keystore.KeyTypeED25519 {
		return nil, fmt.Errorf("unsupported key type %d in '%s'", ks.KeyType, path)
	}

	passphrase := ""
	if ks.IsEncrypted() {
		if p, ok := ks.ReadPassphraseFile(); ok {
			passphrase = p
		} else if p := os.Getenv("PROXIMA_KEY_PASSPHRASE"); p != "" {
			passphrase = p
		} else {
			hint := ""
			if ks.Hint != "" {
				hint = fmt.Sprintf(" (hint: %s)", ks.Hint)
			}
			fmt.Printf("Enter passphrase for '%s'%s: ", path, hint)
			passBytes, err := term.ReadPassword(syscall.Stdin)
			if err != nil {
				return nil, fmt.Errorf("failed to read passphrase: %v", err)
			}
			fmt.Println()
			passphrase = string(passBytes)
		}
	}

	keyBytes, err := ks.GetPrivateKey(passphrase)
	if err != nil {
		return nil, err
	}
	if len(keyBytes) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("key has wrong size: %d (expected %d)", len(keyBytes), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(keyBytes), nil
}

const minPassphraseLength = 10

// ReadPassphraseConfirm prompts for passphrase twice (no-echo) and returns it.
func ReadPassphraseConfirm() string {
	fmt.Print("Enter passphrase: ")
	pass1, err := term.ReadPassword(syscall.Stdin)
	AssertNoError(err)
	fmt.Println()

	Assertf(len(pass1) >= minPassphraseLength, "passphrase must be at least %d characters", minPassphraseLength)

	fmt.Print("Confirm passphrase: ")
	pass2, err := term.ReadPassword(syscall.Stdin)
	AssertNoError(err)
	fmt.Println()

	Assertf(string(pass1) == string(pass2), "passphrases do not match")
	return string(pass1)
}
