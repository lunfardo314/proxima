package main

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
)

const (
	nRepeat  = 2000
	nSecrets = 5
	nBuckets
	scale = 5_000_000
)

func main() {
	secrets := make([]ed25519.PrivateKey, nSecrets)
	pubKeys := make([]ed25519.PublicKey, nSecrets)
	for i := 0; i < nSecrets; i++ {
		secrets[i] = testutil.GetTestingPrivateKey(i)
		pubKeys[i] = secrets[i].Public().(ed25519.PublicKey)
	}

	curProof := make([]byte, 96)
	optionsProofs := make([][]byte, nSecrets)
	buckets := make([]int, nBuckets)

	var slotBin [4]byte
	start := time.Now()

	for i := 0; i < nRepeat; i++ {
		binary.BigEndian.PutUint32(slotBin[:], uint32(i))

		msg := common.Concat(slotBin[:], curProof[:])
		for j := range optionsProofs {
			optionsProofs[j] = ed25519.Sign(secrets[j], msg[:])
		}

		curProof = util.Maximum(optionsProofs, func(proof1, proof2 []byte) bool {
			return ledger.RandomFromSeed(proof1[:], scale) < ledger.RandomFromSeed(proof2[:], scale)
		})
		rnd := ledger.RandomFromSeed(curProof[:], scale)
		//fmt.Printf("%4d      rnd = %s\n", i, util.Th(rnd))

		nBucket := (nBuckets * int(rnd)) / scale
		buckets[nBucket]++
	}
	fmt.Printf("took %v, %.2f millisec/op \n", time.Since(start), float64(time.Since(start)/time.Millisecond)/float64(nRepeat))
	for i := range buckets {
		fmt.Printf("bucket #%d     %4d (%.1f%%)\n", i, buckets[i], float64(buckets[i])*100/float64(nRepeat))
	}
}
