package txmetadata

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

func TestTxMetadata(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		m := &TransactionMetadata{
			SendModeNotPersistent: SourceTypeAPI,
		}
		mBack, err := TransactionMetadataFromBytes(m.Bytes())
		require.NoError(t, err)

		require.EqualValues(t, SourceTypeUndef.String(), mBack.SendModeNotPersistent.String())
		require.EqualValues(t, m.flags(), mBack.flags())
		require.True(t, util.IsNil(mBack.StateRoot))
		require.True(t, mBack.LedgerCoverageDelta == nil)
	})
	t.Run("2", func(t *testing.T) {
		h := blake2b.Sum256([]byte("data"))
		c, err := common.VectorCommitmentFromBytes(ledger.CommitmentModel, h[:])
		require.NoError(t, err)
		m := &TransactionMetadata{
			SendModeNotPersistent: SourceTypeSequencer,
			StateRoot:             c,
		}
		mBack, err := TransactionMetadataFromBytes(m.Bytes())
		require.NoError(t, err)

		require.EqualValues(t, SourceTypeUndef.String(), mBack.SendModeNotPersistent.String())
		require.EqualValues(t, m.flags(), mBack.flags())
		require.True(t, ledger.CommitmentModel.EqualCommitments(m.StateRoot, mBack.StateRoot))
		require.Nil(t, mBack.LedgerCoverageDelta)
	})
	t.Run("3", func(t *testing.T) {
		coverage := uint64(1337)
		m := &TransactionMetadata{
			SendModeNotPersistent: SourceTypeSequencer,
			LedgerCoverageDelta:   &coverage,
		}
		mBack, err := TransactionMetadataFromBytes(m.Bytes())
		require.NoError(t, err)

		require.EqualValues(t, SourceTypeUndef.String(), mBack.SendModeNotPersistent.String())
		require.EqualValues(t, m.flags(), mBack.flags())
		require.Nil(t, mBack.StateRoot)
		require.EqualValues(t, 1337, *mBack.LedgerCoverageDelta)
	})
}
