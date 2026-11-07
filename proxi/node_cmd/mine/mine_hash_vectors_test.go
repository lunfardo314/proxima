package mine

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// hashVectors are BLAKE2b-256 of message || nonce for message bytes 0, 1, 2, ... of the
// given length and the nonce 0x0102030405060708, with message plus nonce ending before, on
// and after a 128-byte block boundary. The Rust seeker's test holds the same values
// (nonce_seeker/src/main.rs, GO_VECTORS), so the two searches agree byte for byte.
var hashVectors = []struct {
	n    int
	want string
}{
	{100, "b7af2281301d649b893e3673679fdf7e61d74dd7347365c17518dc69253506c8"},
	{119, "a3687092de02840d3e8c30e5025d320ac20cc1feb342ba1fba78c60c41c07d3c"},
	{120, "bae38c5df1e42849a52915f23f758ec092485b76e3e5c8ec4e6204c2bef1e6ca"},
	{127, "a1443e852985373cdba3b004be8ab05e20610b869f8389f919b01f537e0c9646"},
	{128, "aaf45eb2daad328c85f92c61f87154454b369f6fbb7c6bffff43d1a305518259"},
	{129, "3f8e4f06a0c13899d168aeb96a93e857a65141a0cf4dee9748324029ecccf17c"},
	{200, "82eba8169dd58a9e6042487be103689d728a36cff1035e2ab1da07196dec3d3d"},
	{247, "20b9eef755e709fd11307bffc52e773d05c058a63134f65166af9c1744d8febc"},
	{248, "ddddc25892c55fe25c48c9660493f7830aa7ae43f29efabcfce2a329c71e2583"},
	{256, "307a1a5b84ff17649608b11a3c701f04ef39548b42e9c432ae1d889fbc56b296"},
	{300, "3a8a0f43b3c3d96fe5466a2f1c751ff06308d08ed409b5a5f4d7d069ef14c604"},
}

// The kept hash state, restored per attempt and finished with the nonce, gives the plain
// BLAKE2b-256 of message and nonce on every side of a block boundary, and the pinned
// values the Rust seeker checks too.
func TestHashTemplateVectors(t *testing.T) {
	var nonce [txbuildercore.MineNonceLen]byte
	binary.BigEndian.PutUint64(nonce[:], 0x0102030405060708)
	for _, v := range hashVectors {
		msg := make([]byte, v.n)
		for i := range msg {
			msg[i] = byte(i)
		}
		plain := blake2b.Sum256(append(append([]byte{}, msg...), nonce[:]...))
		require.Equal(t, v.want, hex.EncodeToString(plain[:]), "pinned vector, length %d", v.n)

		ht, err := hashTemplateOf(msg)
		require.NoError(t, err)
		h, _ := blake2b.New256(nil)
		buf := make([]byte, 0, blake2b.Size256)
		for range 2 { // the hasher and the buffer are reused across attempts, as in the workers
			_, sum := ht.attempt(h, nonce[:], buf)
			require.Equal(t, plain[:], sum, "length %d", v.n)
			work := workKey(sum)
			require.Equal(t, plain[24:32], work[:8], "length %d", v.n)
			require.Equal(t, plain[6:24], work[8:], "length %d", v.n)
		}
	}
}
