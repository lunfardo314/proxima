package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/tuples"
	"golang.org/x/crypto/blake2b"
)

// The extension element of a sequencer output (library upgrade 7), at SeqExtensionFixedIndex:
// inline data holding a serialized tuple, so later upgrades can add elements enforced in
// EasyFL without changing the output's layout. Element 0 is the bid commitment.

// BidCommitmentSize is the size of the sealed-bid commitment: the first 20 bytes of the hash.
const BidCommitmentSize = 20

// BidCommitment is the commitment to a branch bonus, as the ledger recomputes it when the
// branch reveals the bonus and the salt: the first 20 bytes of blake2b-256(bonus || salt),
// the bonus as 8 bytes big-endian.
func BidCommitment(bonus uint64, salt [8]byte) []byte {
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[:8], bonus)
	copy(buf[8:], salt[:])
	h := blake2b.Sum256(buf[:])
	return h[:BidCommitmentSize]
}

// SeqExtensionBytecode is the extension element carrying the commitment: 20 bytes, or nil for
// no bid (a branch then cannot extend the output).
func SeqExtensionBytecode(commitment []byte) []byte {
	t := tuples.EmptyTupleEditable(256)
	t.MustPush(commitment)
	return easyfl.InlineDataBytecode(t.Tuple().Bytes())
}

// SeqBidCommitment returns element 0 of the output's extension element: the bid commitment,
// empty for no bid. An error means the output carries no well-formed extension element.
func SeqBidCommitment(o *Output) ([]byte, error) {
	if o.NumElements() <= SeqExtensionFixedIndex {
		return nil, fmt.Errorf("SeqBidCommitment: the output carries no extension element")
	}
	t, err := tuples.TupleFromBytes(easyfl.StripDataPrefix(o.MustConstraintAt(SeqExtensionFixedIndex)), 256)
	if err != nil {
		return nil, fmt.Errorf("SeqBidCommitment: %w", err)
	}
	if t.NumElements() == 0 {
		return nil, fmt.Errorf("SeqBidCommitment: the extension element holds no commitment")
	}
	return t.MustAt(0), nil
}
