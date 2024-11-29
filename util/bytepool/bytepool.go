package bytepool

import (
	"sync"

	"github.com/lunfardo314/proxima/util"
)

// Optimization of memory allocation and GC

// bytePool[i] contains pooled slices with cap() < 1 << (i + 6)
// bytePool[0] for slices with < cap(64)
// bytePool[1] for slices with < cap(128)
// bytePool[2] for slices with < cap(256)
// bytePool[10] for slices with < cap(2^16)

const maxPools = 11

var bytePool [maxPools]sync.Pool

func poolIndexByCapacity(capacity int) int {
	capacity >>= 6
	for i := 0; i < maxPools; i++ {
		if capacity == 0 {
			return i
		}
		capacity >>= 1
	}
	return -1
}

func DisposeArray(s []byte) {
	poolIndex := poolIndexByCapacity(cap(s))
	if poolIndex < 0 {
		// very big chunks -> feed to GC
		return
	}
	util.Assertf(poolIndex < maxPools, "poolIndex < maxPools")
	bytePool[poolIndex].Put(s)
}

// GetArray always returns slice as make([]byte, capacity), but first checks in the pool to reuse
// Not filled with zeroes!
func GetArray(capacity int) []byte {
	poolIndex := poolIndexByCapacity(capacity)
	if poolIndex < 0 {
		return make([]byte, capacity)
	}

	for i := poolIndex; i < maxPools; i++ {
		if r := bytePool[i].Get(); r != nil {
			ret := r.([]byte)
			if len(ret) >= capacity {
				return ret[:capacity]
			}
			return append(ret, make([]byte, capacity-len(ret))...)
		}
	}
	return make([]byte, capacity)
}
