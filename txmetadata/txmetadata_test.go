package txmetadata

import (
	"testing"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

func TestTxMetadata(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		m := &TransactionMetadata{
			SendType: SendTypeFromAPISource,
		}
		mBack, err := TransactionMetadataFromBytes(m.Bytes())
		require.NoError(t, err)

		require.EqualValues(t, m.SendType, mBack.SendType)
		require.True(t, util.IsNil(mBack.StateRoot))
	})
	t.Run("2", func(t *testing.T) {
		h := blake2b.Sum256([]byte("data"))
		c, err := common.VectorCommitmentFromBytes(core.CommitmentModel, h[:])
		require.NoError(t, err)
		m := &TransactionMetadata{
			SendType:  SendTypeFromSequencer,
			StateRoot: c,
		}
		mBack, err := TransactionMetadataFromBytes(m.Bytes())
		require.NoError(t, err)

		require.EqualValues(t, m.SendType, mBack.SendType)
		require.True(t, core.CommitmentModel.EqualCommitments(m.StateRoot, mBack.StateRoot))
	})
}
