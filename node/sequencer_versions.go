package node

// Sequencer versions compiled into the node. Each registers itself in init() and the
// operator picks one with `sequencer.version`. A new version is added here.
import (
	_ "github.com/lunfardo314/proxima/sequencer/v1"
	_ "github.com/lunfardo314/proxima/sequencer/v2"
)
