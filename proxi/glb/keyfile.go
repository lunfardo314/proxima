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
		passphrase = os.Getenv("PROXIMA_KEY_PASSPHRASE")
		if passphrase == "" {
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

// ReadPassphraseConfirm prompts for passphrase twice (no-echo) and returns it.
func ReadPassphraseConfirm() string {
	fmt.Print("Enter passphrase: ")
	pass1, err := term.ReadPassword(syscall.Stdin)
	AssertNoError(err)
	fmt.Println()

	fmt.Print("Confirm passphrase: ")
	pass2, err := term.ReadPassword(syscall.Stdin)
	AssertNoError(err)
	fmt.Println()

	Assertf(string(pass1) == string(pass2), "passphrases do not match")
	Assertf(len(pass1) > 0, "passphrase must not be empty")
	return string(pass1)
}
