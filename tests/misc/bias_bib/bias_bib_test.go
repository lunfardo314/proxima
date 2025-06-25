package bias_bib

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/crypto/blake2b"
)

const (
	fname    = "branch_inflation.txt"
	nBuckets = 100
)

func TestBias(t *testing.T) {
	file, err := os.Open(fname)
	util.AssertNoError(err)
	defer util.MustClose(file)

	bucketsVrf := make([]int, nBuckets)
	bucketsOrd := make([]int, nBuckets)

	// Create a scanner to read the file line by line
	scanner := bufio.NewScanner(file)

	count := 0
	var minBib, maxBib uint64

	fout, err := os.Create("strings.txt")
	util.AssertNoError(err)
	defer util.MustClose(fout)

	for scanner.Scan() {

		//var data [8]byte
		str := fmt.Sprintf("text  %d", count)
		//binary.BigEndian.PutUint64(data[:], uint64(rand.Int31()))
		//h := blake2b.Sum256(data[:])
		h := blake2b.Sum256([]byte(str))
		v := binary.BigEndian.Uint64(h[:8]) % 5_000_001
		bucketNo := (nBuckets * int(v)) / 5_000_001
		bucketsOrd[bucketNo]++

		line := scanner.Text()
		// Split the line into fields separated by spaces
		fields := strings.Fields(line)
		vrfProof, err := hex.DecodeString(fields[2])
		util.AssertNoError(err)

		//_, _ = fmt.Fprintf(fout, "%s\n", fields[2])

		providedBib, err := strconv.Atoi(fields[3])
		util.AssertNoError(err)

		h = blake2b.Sum256(vrfProof)
		num := binary.BigEndian.Uint64(h[:8])
		calculatedBib := num % (5_000_001)
		util.Assertf(uint64(providedBib) == calculatedBib, "providedBib(%d)==calculatedBib(%d)", providedBib, calculatedBib)
		fmt.Printf("%s\n     %s\n", fields[2], util.Th(calculatedBib))
		count++
		bucketNo = (nBuckets * int(calculatedBib)) / 5_000_001
		bucketsVrf[bucketNo]++
		if minBib == 0 {
			minBib = calculatedBib
		} else {
			minBib = min(minBib, calculatedBib)
		}
		maxBib = max(maxBib, calculatedBib)
		//if count == 1000 {
		//	break
		//}
	}
	if err := scanner.Err(); err != nil {
		fmt.Println("Error reading file:", err)
	}
	fmt.Printf("count: %d\n", count)
	fmt.Printf("min: %s\n", util.Th(minBib))
	fmt.Printf("max: %s\n", util.Th(maxBib))
	for i, v := range bucketsVrf {
		fmt.Printf("#%2d   %d (%.2f%%)\n", i, v, float64(v)/float64(count)*100.0)
	}
	fmt.Printf("-----------------------------------------------------\n")
	for i, v := range bucketsOrd {
		fmt.Printf("#%2d   %d (%.2f%%)\n", i, v, float64(v)/float64(count)*100.0)
	}
}

const (
	//nBuckets1 = 100
	fname1 = "strings.txt"
)

func TestBias1(t *testing.T) {
	file, err := os.Open("strings.txt")
	util.AssertNoError(err)
	defer util.MustClose(file)

	bucketsVrf := make([]int, 100)

	// Create a scanner to read the file line by line
	scanner := bufio.NewScanner(file)

	count := 0
	for scanner.Scan() {
		line := scanner.Text()

		lineBin, err := hex.DecodeString(line)
		util.AssertNoError(err)

		h := blake2b.Sum256(lineBin)
		//h := blake2b.Sum512(lineBin)
		num := binary.BigEndian.Uint64(h[:8])
		v := num % (5_000_001)
		count++
		bucketNo := (100 * int(v)) / 5_000_001
		bucketsVrf[bucketNo]++
	}
	fmt.Printf("count: %d\n", count)
	for i, v := range bucketsVrf {
		fmt.Printf("#%2d   %d (%.2f%%)\n", i, v, float64(v)/float64(count)*100.0)
	}
}

func TestBias2(t *testing.T) {
	s := set.New[string]()
	s1 := set.New[string]()
	file, err := os.Open(fname1)
	util.AssertNoError(err)
	defer util.MustClose(file)

	// Create a scanner to read the file line by line
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		util.Assertf(s.InsertNew(scanner.Text()), "duplicate 1")
		util.Assertf(s1.InsertNew(scanner.Text()[:16]), "duplicate 2")
	}
}

func TestScaling(t *testing.T) {
	file, err := os.Open("vrf3.txt")
	util.AssertNoError(err)
	defer util.MustClose(file)

	bucketsVrf := make([]int, 10)

	// Create a scanner to read the file line by line
	scanner := bufio.NewScanner(file)

	count := 0
	for scanner.Scan() {
		line := scanner.Text()

		lineBin, err := hex.DecodeString(line)
		util.AssertNoError(err)
		v := ledger.RandomFromSeed(lineBin, 5_000_000)
		count++
		bucketNo := (10 * int(v)) / 5_000_000
		bucketsVrf[bucketNo]++
	}
	fmt.Printf("count: %d\n", count)
	for i, v := range bucketsVrf {
		fmt.Printf("#%2d   %d (%.2f%%)\n", i, v, float64(v)/float64(count)*100.0)
	}
}
