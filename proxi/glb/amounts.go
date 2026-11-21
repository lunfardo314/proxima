package glb

import (
	"fmt"

	"github.com/lunfardo314/proxima/util"
)

// ProxAndMotes renders an amount the user typed in motes, as PROX and in motes,
// so a prompt shows both the unit of the input and what it means.
func ProxAndMotes(motes uint64) string {
	return fmt.Sprintf("%s (%s motes)", util.Prox(motes), util.Th(motes, ","))
}
