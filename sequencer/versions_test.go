package sequencer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// fakeSequencer records which version constructed it.
type fakeSequencer struct{ version string }

func (f *fakeSequencer) Start()                                      {}
func (f *fakeSequencer) SequencerID() base.ChainID                   { return base.ChainID{} }
func (f *fakeSequencer) LedgerCoverage() uint64                      { return 0 }
func (f *fakeSequencer) ConsensusContribution() uint64               { return 0 }
func (f *fakeSequencer) IsVertexReferenced(_ *vertex.WrappedTx) bool { return false }

func fakeConstructor(version string) Constructor {
	return func(_ *workflow.Workflow, _ base.ChainID, _ ...ConfigOption) (Sequencer, error) {
		return &fakeSequencer{version: version}, nil
	}
}

// TestNewFromConfigSelectsVersion checks that `sequencer.version` picks the registered
// constructor, that an absent key means DefaultVersion, that an unknown name is an error
// and that a disabled sequencer is not built at all.
func TestNewFromConfigSelectsVersion(t *testing.T) {
	RegisterVersion(DefaultVersion, fakeConstructor(DefaultVersion))
	RegisterVersion("test-v2", fakeConstructor("test-v2"))
	t.Cleanup(func() {
		delete(versions, DefaultVersion)
		delete(versions, "test-v2")
		viper.Reset()
	})
	require.Panics(t, func() { RegisterVersion("test-v2", fakeConstructor("again")) })

	keyFile := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(keyFile, []byte("x"), 0600))
	viper.Set("sequencer.enable", true)
	viper.Set("sequencer.chain_id", util.Ref(base.ChainID{}).StringHex())
	viper.Set("sequencer.controller_key_file", keyFile)

	versionOf := func() string {
		seq, err := NewFromConfig(nil)
		require.NoError(t, err)
		return seq.(*fakeSequencer).version
	}
	require.Equal(t, DefaultVersion, versionOf())

	viper.Set("sequencer.version", "test-v2")
	require.Equal(t, "test-v2", versionOf())

	viper.Set("sequencer.version", "nope")
	_, err := NewFromConfig(nil)
	require.ErrorContains(t, err, "unknown sequencer version 'nope'")

	viper.Set("sequencer.enable", false)
	seq, err := NewFromConfig(nil)
	require.NoError(t, err)
	require.Nil(t, seq)
}
