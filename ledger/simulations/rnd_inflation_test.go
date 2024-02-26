package simulations

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
)

const maxInflationValue = ledger.DefaultMaxBranchInflationBonus

// I is inflation, 0 <= I <= maxInflationValue
// data = some data with I
// constraint: I <= hash(data) % maxInflationValue + 1

func mineConstraint(rndData []byte, target uint64) int {
	var targetBin, variableBin [8]byte
	binary.BigEndian.PutUint64(targetBin[:], target)

	for variable := uint64(0); ; variable++ {
		binary.BigEndian.PutUint64(variableBin[:], variable)
		data := common.Concat(rndData, targetBin[:], variableBin[:])
		h := blake2b.Sum256(data)
		value := binary.BigEndian.Uint64(h[:8])
		if target <= value%maxInflationValue+1 {
			return int(variable)
		}
	}
}

func TestRandomInflation(t *testing.T) {
	const n = 5
	var rndData [200]byte
	from := maxInflationValue - maxInflationValue/100000
	step := (maxInflationValue - from) / 10
	for i := 0; i <= n; i++ {
		rand.Read(rndData[:])
		fmt.Printf("-------------- %d\n", i)
		for target := from; target <= maxInflationValue; target += step {
			steps := mineConstraint(rndData[:], uint64(target))
			fmt.Printf("inflation target: %s, steps: %d\n", util.GoTh(target), steps)
		}
	}
}
