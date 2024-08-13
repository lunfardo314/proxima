package multistate

import (
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/unitrie/common"
)

// WriteState writes state with the root as a sequence of key/value pairs.
// Does not write ledger identity record
func WriteState(state global.StateStoreReader, target common.KVStreamWriter, root common.VCommitment) error {
	rdr, err := NewReadable(state, root)
	if err != nil {
		return fmt.Errorf("WriteState: %w", err)
	}
	iter := rdr.Iterator(nil)

	iter.Iterate(func(k, v []byte) bool {
		if len(k) > 0 {
			// skip ledger identity record
			err = target.Write(k, v)
		}
		return err == nil
	})
	return err
}
