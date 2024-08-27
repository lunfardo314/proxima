package simulations

import (
	"crypto"
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
)

const maxInflationValue = ledger.DefaultBranchInflationBonusBase

// I is the inflation, 0 <= I <= maxInflationValue
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

func TestRandomInflation1(t *testing.T) {
	const n = 5
	var rndData [200]byte

	from := maxInflationValue - maxInflationValue/100000
	step := (maxInflationValue - from) / 10
	for i := 0; i <= n; i++ {
		rand.Read(rndData[:])
		fmt.Printf("-------------- %d\n", i)
		for target := from; target <= maxInflationValue; target += step {
			steps := mineConstraint(rndData[:], uint64(target))
			fmt.Printf("inflation target: %s, steps: %d\n", util.Th(target), steps)
		}
	}
}

var rnd = rand.New(rand.NewSource(time.Now().UnixNano()))

func TestRandomInflation2(t *testing.T) {
	cycles := 1
	var rndData [200]byte
	rand.Read(rndData[:])
	var binInt [8]byte

	privKey := testutil.GetTestingPrivateKey(100)
	pubKey := privKey.Public().(ed25519.PublicKey)

	start := time.Now()
	var vardata [32]byte
	for best := 0; best < maxInflationValue; cycles++ {
		candidate := best + rand.Intn(maxInflationValue-best) + 1
		binary.BigEndian.PutUint64(binInt[:], uint64(candidate))
		//rand.Read(vardata[:])
		binary.BigEndian.PutUint64(vardata[:8], uint64(cycles))
		data := common.Concat(rndData[:], binInt[:], vardata[:])
		sig, err := privKey.Sign(rnd, data, crypto.Hash(0))
		util.AssertNoError(err)
		dataWithSig := common.Concat(data, sig, []byte(pubKey))

		h := blake2b.Sum256(dataWithSig)
		since := time.Since(start)
		if uint64(candidate) <= binary.BigEndian.Uint64(h[:8])%maxInflationValue+1 {
			best = candidate
			fmt.Printf("%s cycles %d, %v, %dus/cycle\n", util.Th(best), cycles, since, int(since/time.Microsecond)/cycles)
		}
		if cycles%500_000 == 0 {
			fmt.Printf("                   %.1f mln cycles, %dus/cycle\n", float32(cycles)/1_000_000, int(since/time.Microsecond)/cycles)
		}
	}
}

func TestConstantInflation(t *testing.T) {
	const (
		initialSupply           = ledger.DefaultInitialSupply / 1000
		annualInflationRatePerc = 10
	)
	rate := 1 + float64(annualInflationRatePerc)/100
	infl := float64(initialSupply)
	for i := 0; infl < float64(math.MaxUint64) && rate > 0; i++ {
		fmt.Printf("%3d : %20s      %2f\n", i, util.Th(uint64(infl)), rate)
		infl = infl * rate
		rate = rate - 0.001
	}
}
