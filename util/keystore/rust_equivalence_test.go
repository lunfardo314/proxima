package keystore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

// Equivalence of this package with its Rust twin in rust/ (crate
// proxima-keystore), checked through the crate's keystore_tool binary: key
// files written by either side are read by the other, encrypted and not, with
// the same passphrase rules, the same key check and the same passphrase file
// lookup. Skipped when cargo is not installed.

func hexText(s string) string {
	if s == "" {
		return "-"
	}
	return hex.EncodeToString([]byte(s))
}

func TestRustEquivalence(t *testing.T) {
	bin := testutil.CargoBin(t, "rust", "keystore_tool")
	dir := t.TempDir()
	tool := testutil.StartLineTool(t, bin, dir)
	call := func(format string, args ...any) string {
		return tool.Call(t, fmt.Sprintf(format, args...))
	}
	in := func(name string) string { return filepath.Join(dir, name) }

	cases := []struct{ passphrase, holder, hint string }{
		{"a passphrase of some length", "holder-1", ""},
		{"pass with spaces and ünïcödé", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "the hint, with spaces"},
		{"x", "", "h"}, // the shortest passphrase either side accepts
	}
	for i, c := range cases {
		pk, sk, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		skHex, pkHex := hex.EncodeToString(sk), hex.EncodeToString(pk)

		// Go encrypts, Rust loads; a wrong passphrase fails there too
		ks, err := Encrypt(KeyTypeED25519, sk, pk, c.passphrase, c.holder)
		require.NoError(t, err)
		ks.Hint = c.hint
		goEnc := in(fmt.Sprintf("go_enc_%d.key", i))
		require.NoError(t, ks.SaveToFile(goEnc))
		resp := call("load %s %s", goEnc, hexText(c.passphrase))
		require.Equal(t, fmt.Sprintf("%s 1 %s %s %s", skHex, pkHex, hexText(c.holder), hexText(c.hint)), resp)
		require.True(t, strings.HasPrefix(call("load %s %s", goEnc, hexText("wrong")), "ERR"))

		// Rust encrypts, Go loads
		rustEnc := in(fmt.Sprintf("rust_enc_%d.key", i))
		require.Equal(t, "OK", call("encrypt %s %s %s %s %s %s", rustEnc, skHex, pkHex, hexText(c.passphrase), hexText(c.holder), hexText(c.hint)))
		loaded, err := LoadFromFile(rustEnc)
		require.NoError(t, err)
		require.True(t, loaded.IsEncrypted())
		require.Equal(t, c.holder, loaded.HolderID)
		require.Equal(t, c.hint, loaded.Hint)
		require.Equal(t, pkHex, loaded.PublicKey)
		got, err := loaded.GetPrivateKey(c.passphrase)
		require.NoError(t, err)
		require.Equal(t, []byte(sk), got)
		_, err = loaded.GetPrivateKey("wrong")
		require.Error(t, err)
		// the KDF parameters Rust writes are the ones Go writes
		require.Equal(t, "aes-256-gcm", loaded.Crypto.Cipher)
		require.Equal(t, "argon2id", loaded.Crypto.KDF)
		require.EqualValues(t, defaultArgonTime, loaded.Crypto.KDFParams.Time)
		require.EqualValues(t, defaultArgonMemory, loaded.Crypto.KDFParams.Memory)
		require.EqualValues(t, defaultArgonThreads, loaded.Crypto.KDFParams.Threads)
		require.Len(t, loaded.Crypto.KDFParams.Salt, 2*saltSize)
		require.Len(t, loaded.Crypto.Nonce, 2*nonceSize)

		// unencrypted, both ways; the passphrase is ignored
		plain, err := NewUnencrypted(KeyTypeED25519, sk, pk, c.holder)
		require.NoError(t, err)
		goPlain := in(fmt.Sprintf("go_plain_%d.key", i))
		require.NoError(t, plain.SaveToFile(goPlain))
		require.Equal(t, fmt.Sprintf("%s 0 %s %s -", skHex, pkHex, hexText(c.holder)), call("load %s %s", goPlain, hexText("ignored")))
		rustPlain := in(fmt.Sprintf("rust_plain_%d.key", i))
		require.Equal(t, "OK", call("unencrypted %s %s %s %s", rustPlain, skHex, pkHex, hexText(c.holder)))
		loaded, err = LoadFromFile(rustPlain)
		require.NoError(t, err)
		require.False(t, loaded.IsEncrypted())
		got, err = loaded.GetPrivateKey("")
		require.NoError(t, err)
		require.Equal(t, []byte(sk), got)

		// Rust re-encrypts a Go plain file and decrypts a Go encrypted one
		reEnc := in(fmt.Sprintf("rust_reenc_%d.key", i))
		require.Equal(t, "OK", call("encrypt_file %s %s %s %s", goPlain, reEnc, hexText(c.passphrase), hexText(c.hint)))
		loaded, err = LoadFromFile(reEnc)
		require.NoError(t, err)
		require.Equal(t, c.hint, loaded.Hint)
		got, err = loaded.GetPrivateKey(c.passphrase)
		require.NoError(t, err)
		require.Equal(t, []byte(sk), got)
		dec := in(fmt.Sprintf("rust_dec_%d.key", i))
		require.Equal(t, "OK", call("decrypt_file %s %s %s", goEnc, dec, hexText(c.passphrase)))
		loaded, err = LoadFromFile(dec)
		require.NoError(t, err)
		require.False(t, loaded.IsEncrypted())
		require.Equal(t, skHex, loaded.PrivateKey)
		require.True(t, strings.HasPrefix(call("decrypt_file %s %s %s", goEnc, dec, hexText("wrong")), "ERR"))
	}

	// a corrupted public key is caught by both key checks
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	otherPk, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	bad, err := NewUnencrypted(KeyTypeED25519, sk, otherPk, "h")
	require.NoError(t, err)
	badPath := in("bad.key")
	require.NoError(t, bad.SaveToFile(badPath))
	_, err = bad.GetPrivateKey("")
	require.Error(t, err)
	require.True(t, strings.HasPrefix(call("load %s -", badPath), "ERR"))

	// the passphrase file: named after the holder ID in the working directory,
	// content trimmed; absent means none, on both sides
	ks, err := Encrypt(KeyTypeED25519, sk, pk, "secret", "holderfile")
	require.NoError(t, err)
	ksPath := in("pf.key")
	require.NoError(t, ks.SaveToFile(ksPath))
	t.Chdir(dir)
	_, ok := ks.ReadPassphraseFile()
	require.False(t, ok)
	require.Equal(t, "NONE", call("passfile %s", ksPath))
	require.NoError(t, os.WriteFile(in("holderfile"), []byte("  from file \n"), 0o600))
	p, ok := ks.ReadPassphraseFile()
	require.True(t, ok)
	require.Equal(t, "from file", p)
	require.Equal(t, hexText("from file"), call("passfile %s", ksPath))

	// is-keystore agrees on a keystore, a non-JSON file and a missing one
	notJSON := in("not.json")
	require.NoError(t, os.WriteFile(notJSON, []byte("hello"), 0o600))
	for _, f := range []string{ksPath, notJSON, in("missing.key")} {
		want := "0"
		if IsKeystoreFile(f) {
			want = "1"
		}
		require.Equal(t, want, call("iskeystore %s", f), f)
	}
}
