package attacher

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/util/set"
)

type pastCone struct {
	vertices   map[*vertex.WrappedTx]Flags
	rooted     map[*vertex.WrappedTx]set.Set[byte]
	referenced referencedSet
}

func newPastCone() *pastCone {
	return &pastCone{
		vertices:   make(map[*vertex.WrappedTx]Flags),
		rooted:     make(map[*vertex.WrappedTx]set.Set[byte]),
		referenced: newReferencedSet(),
	}
}
