package txbuilder_seq

import (
	"encoding/binary"

	"golang.org/x/crypto/blake2b"
)

// bidSalt is the salt of the bid committed on this sequencer's milestones of a slot (library
// upgrade 7). It is derived from the sequencer's private key and the slot, so the branch that
// reveals it needs no record of what was committed, and nobody else can compute it.
func bidSalt(privateKey []byte, slot uint32) (ret [8]byte) {
	var slotBin [4]byte
	binary.BigEndian.PutUint32(slotBin[:], slot)
	h := blake2b.Sum256(append(append([]byte("proxima bid salt"), privateKey...), slotBin[:]...))
	copy(ret[:], h[:8])
	return
}
