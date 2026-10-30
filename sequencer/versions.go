package sequencer

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/spf13/viper"
)

type (
	// Sequencer is what the node needs from a running sequencer of any version.
	Sequencer interface {
		Start()
		SequencerID() base.ChainID
		LedgerCoverage() uint64
		ConsensusContribution() uint64
		IsVertexReferenced(vid *vertex.WrappedTx) bool
	}

	// Constructor creates a sequencer of one version. The controller key is loaded by the
	// sequencer itself at start, from ConfigOptions.ControllerKeyFile.
	Constructor func(glb *workflow.Workflow, seqID base.ChainID, opts ...ConfigOption) (Sequencer, error)
)

// DefaultVersion is the version started when `sequencer.version` is absent from the config.
const DefaultVersion = "v1"

// versions is written only from init() of the version packages, so it needs no lock.
var versions = make(map[string]Constructor)

// RegisterVersion makes a sequencer version selectable by name in `sequencer.version`.
// Called from init() of the version's package.
func RegisterVersion(name string, constructor Constructor) {
	if _, already := versions[name]; already {
		panic(fmt.Sprintf("sequencer version '%s' registered twice", name))
	}
	versions[name] = constructor
}

// Versions returns the names of the registered versions, sorted.
func Versions() []string {
	ret := make([]string, 0, len(versions))
	for name := range versions {
		ret = append(ret, name)
	}
	sort.Strings(ret)
	return ret
}

// VersionFromConfig is the version named in `sequencer.version`, DefaultVersion when absent.
func VersionFromConfig() string {
	if version := viper.GetString("sequencer.version"); version != "" {
		return version
	}
	return DefaultVersion
}

// NewFromConfig builds the sequencer of the version named in `sequencer.version`.
// Returns nil, nil when the sequencer is not configured or disabled.
// extraOpts are appended after the config-derived options, so they win on last-write
// fields; the node uses them to force-start the sequencer in the bootstrap-from-old-state
// case (sync_semantics.md).
func NewFromConfig(glb *workflow.Workflow, extraOpts ...ConfigOption) (Sequencer, error) {
	cfg, seqID, err := paramsFromConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	version := VersionFromConfig()
	constructor, ok := versions[version]
	if !ok {
		return nil, fmt.Errorf("unknown sequencer version '%s', available: %v", version, Versions())
	}
	return constructor(glb, seqID, append(cfg, extraOpts...)...)
}
