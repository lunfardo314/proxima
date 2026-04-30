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
	t.Run("0", func(t *testing.T) {
		m := &TransactionMetadata{}
		mb := m.Bytes()
		require.EqualValues(t, []byte{0}, mb)
		mBack, err := TransactionMetadataFromBytes(mb)
		require.NoError(t, err)
		require.Nil(t, mBack)
	})
	t.Run("1", func(t *testing.T) {
		m := &TransactionMetadata{
			SourceTypeNonPersistent: SourceTypeAPI,
		}
		mBack, err := TransactionMetadataFromBytes(m.Bytes())
		require.NoError(t, err)
		require.Nil(t, mBack)
	})
	t.Run("2", func(t *testing.T) {
		h := blake2b.Sum256([]byte("data"))
		c, err := common.VectorCommitmentFromBytes(ledger.CommitmentModel, h[:ledger.TrieHashSize])
		require.NoError(t, err)
		m := &TransactionMetadata{
			SourceTypeNonPersistent: SourceTypeSequencer,
			StateRoot:               c,
		}
		mBack, err := TransactionMetadataFromBytes(m.Bytes())
		require.NoError(t, err)

		require.EqualValues(t, SourceTypeUndef.String(), mBack.SourceTypeNonPersistent.String())
		require.EqualValues(t, m.flags(), mBack.flags())
		require.True(t, ledger.CommitmentModel.EqualCommitments(m.StateRoot, mBack.StateRoot))
		require.Nil(t, mBack.CoverageDelta)
		require.Nil(t, mBack.LedgerCoverage)
	})
	t.Run("3", func(t *testing.T) {
		coverage := uint64(1337)
		coverageDelta := uint64(222)
		inflation := uint64(31415)
		supply := uint64(2718281828)
		m := &TransactionMetadata{
			SourceTypeNonPersistent: SourceTypeSequencer,
			CoverageDelta:           &coverageDelta,
			LedgerCoverage:          &coverage,
			SlotInflation:           &inflation,
			Supply:                  &supply,
		}
		mBack, err := TransactionMetadataFromBytes(m.Bytes())
		require.NoError(t, err)

		require.EqualValues(t, SourceTypeUndef.String(), mBack.SourceTypeNonPersistent.String())
		require.EqualValues(t, m.flags(), mBack.flags())
		require.Nil(t, mBack.StateRoot)
		require.EqualValues(t, coverage, *mBack.LedgerCoverage)
		require.EqualValues(t, coverageDelta, *mBack.CoverageDelta)
		require.EqualValues(t, inflation, *mBack.SlotInflation)
		require.EqualValues(t, supply, *mBack.Supply)
	})
	t.Run("4", func(t *testing.T) {
		coverage := uint64(1337)
		coverageDelta := uint64(222)
		inflation := uint64(31415)
		supply := uint64(2718281828)
		m := &TransactionMetadata{
			SourceTypeNonPersistent: SourceTypeSequencer,
			CoverageDelta:           &coverageDelta,
			LedgerCoverage:          &coverage,
			SlotInflation:           &inflation,
			Supply:                  &supply,
		}
		mBack, err := TransactionMetadataFromBytes(m.Bytes())
		require.NoError(t, err)

		require.EqualValues(t, SourceTypeUndef.String(), mBack.SourceTypeNonPersistent.String())
		require.EqualValues(t, m.flags(), mBack.flags())
		require.Nil(t, mBack.StateRoot)
		require.EqualValues(t, 222, *mBack.CoverageDelta)
		require.EqualValues(t, 1337, *mBack.LedgerCoverage)
		require.EqualValues(t, 31415, *mBack.SlotInflation)
		require.EqualValues(t, 2718281828, *mBack.Supply)
	})
	t.Run("5", func(t *testing.T) {
		coverageDelta := uint64(222)
		inflation := uint64(31415)
		supply := uint64(2718281828)
		m := &TransactionMetadata{
			SourceTypeNonPersistent: SourceTypeSequencer,
			CoverageDelta:           &coverageDelta,
			SlotInflation:           &inflation,
			Supply:                  &supply,
		}
		mBack, err := TransactionMetadataFromBytes(m.Bytes())
		require.NoError(t, err)

		require.EqualValues(t, SourceTypeUndef.String(), mBack.SourceTypeNonPersistent.String())
		require.EqualValues(t, m.flags(), mBack.flags())
		require.Nil(t, mBack.StateRoot)
		require.EqualValues(t, 222, *mBack.CoverageDelta)
		require.True(t, util.IsNil(mBack.LedgerCoverage))
		require.EqualValues(t, 31415, *mBack.SlotInflation)
		require.EqualValues(t, 2718281828, *mBack.Supply)
	})
	t.Run("6", func(t *testing.T) {
		coverageDelta := uint64(222)
		inflation := uint64(31415)
		supply := uint64(2718281828)
		m := &TransactionMetadata{
			SourceTypeNonPersistent: SourceTypeSequencer,
			CoverageDelta:           &coverageDelta,
			SlotInflation:           &inflation,
			Supply:                  &supply,
		}
		t.Logf("--------------\n%s", m.Lines("       ").String())
		m.LedgerCoverage = util.Ref(uint64(1337))
		t.Logf("--------------\n%s", m.Lines("       ").String())
	})

}
