package testutil

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

type Integer interface {
	int | uint16 | uint32 | uint64 | int16 | int32 | int64
}

var prn = message.NewPrinter(language.English)

func GoThousands[T Integer](v T) string {
	return strings.Replace(prn.Sprintf("%d", v), ",", "_", -1)
}

const TestingDeterministicOriginPrivateKey = "8ec47313c15c3a4443c41619735109b56bc818f4a6b71d6a1f186ec96d15f28f14117899305d99fb4775de9223ce9886cfaa3195da1e40c5db47c61266f04dd2"

func GetTestingPrivateKey(idx ...int) ed25519.PrivateKey {
	var ret ed25519.PrivateKey
	if len(idx) == 0 {
		pkBin, err := hex.DecodeString(TestingDeterministicOriginPrivateKey)
		ret = pkBin
		util.AssertNoError(err)
	} else {
		var u64 [8]byte
		binary.BigEndian.PutUint64(u64[:], uint64(idx[0]))
		seed := blake2b.Sum256(common.Concat([]byte(TestingDeterministicOriginPrivateKey), u64[:]))
		ret = ed25519.NewKeyFromSeed(seed[:])
	}
	return ret
}

func GetTestingPrivateKeys(n int, offsIndex ...int) []ed25519.PrivateKey {
	offs := 31415
	if len(offsIndex) > 0 {
		offs = offsIndex[0]
	}
	ret := make([]ed25519.PrivateKey, n)
	for i := range ret {
		ret[i] = GetTestingPrivateKey(offs + i + 1)
	}
	return ret
}

func PrintRTStatsForSomeTime(d time.Duration) {
	var mstats runtime.MemStats

	deadline := time.Now().Add(d)
	for {
		if time.Now().After(deadline) {
			return
		}
		runtime.ReadMemStats(&mstats)
		fmt.Printf(">>>>> alloc: %.1f MB, goroutines: %d\n", float32(mstats.Alloc*10/(1024*1024))/10, runtime.NumGoroutine())
		time.Sleep(500 * time.Millisecond)
	}
}

func LogToFile(fname, format string, args ...interface{}) {
	f, err := os.OpenFile(fname,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	util.AssertNoError(err)

	defer f.Close()

	_, err = f.WriteString(fmt.Sprintf(format, args...))
	util.AssertNoError(err)
	_, err = f.WriteString("\n")
	util.AssertNoError(err)
}
