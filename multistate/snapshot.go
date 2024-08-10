package multistate

import (
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/unitrie/common"
)

// WriteSnapshot writes state with the root as a sequence of key/value pairs
func WriteSnapshot(state global.StateStoreReader, target common.KVStreamWriter, root common.VCommitment) error {
	rdr, err := NewReadable(state, root)
	if err != nil {
		return fmt.Errorf("WriteSnapshot: %w", err)
	}
	iter := rdr.Iterator(nil)

	iter.Iterate(func(k, v []byte) bool {
		err = target.Write(k, v)
		return err == nil
	})
	return err
}
