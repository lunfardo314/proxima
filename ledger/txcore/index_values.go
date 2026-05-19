package txcore

import "github.com/lunfardo314/easyfl/tuples"

// EncodeIndexValuesTuple serialises a list of index values into the
// wire form of the index-value tuple stored at output slot 1
// (ConstraintIndexIndexValues).
//
// Empty input → empty bytes (no tuple), which the parser reads as
// "this UTXO is not indexed". Non-empty inputs are written as a
// tuple of the given byte slices, in order.
//
// The §4.1 master-first convention puts the controlling/sender holder
// at position 0; subsequent positions carry kind-specific extras
// (e.g. tagAlong puts targetSequencerID at position 1).
func EncodeIndexValuesTuple(values [][]byte) []byte {
	if len(values) == 0 {
		return nil
	}
	t := tuples.EmptyTupleEditable(MaxNumConstraints)
	for _, v := range values {
		t.MustPush(v)
	}
	return t.Tuple().Bytes()
}
