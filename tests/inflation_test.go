package tests

import (
	"math"
	"testing"

	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/util"
)

func requireBits(n uint64) (ret int) {
	for ; n != 0; ret++ {
		n >>= 1
	}
	return
}

func TestInflationCalculations(t *testing.T) {
	t.Logf("MaxUint64: %s, requireBits: %d", util.GoTh(uint64(math.MaxUint64)), requireBits(uint64(math.MaxUint64)))
	t.Logf("Proxima default supply: %s, requireBits: %d", util.GoTh(genesis.DefaultSupply), requireBits(genesis.DefaultSupply))
	const (
		SatoshiInBTC = 100_000_000
		MaxBTCApprox = 21_000_000
	)
	t.Logf("Max Bitcoin supply. BTC: %s", util.GoTh(MaxBTCApprox))
	t.Logf("Max Bitcoin supply. Satoshi: %s, , requireBits: %d", util.GoTh(MaxBTCApprox*SatoshiInBTC), requireBits(MaxBTCApprox*SatoshiInBTC))

	const (
		InitialSupplyProxi   = 2_000_000_000
		DustPerProxi         = 1_000_000
		InitialProximaSupply = InitialSupplyProxi * DustPerProxi
	)
	t.Logf("Proxima supply dust: %s, requireBits: %d", util.GoTh(InitialProximaSupply), requireBits(InitialProximaSupply))

}
