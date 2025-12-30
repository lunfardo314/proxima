package diskusage

import (
	"testing"
)

func TestGetDiskUsage(t *testing.T) {
	total, available, free := GetDiskUsage("/")
	t.Logf("Total space: %.2f GB\n", float64(total)/(1<<30))
	t.Logf("Available space: %.2f GB\n", float64(available)/(1<<30))
	t.Logf("Free space: %.2f GB\n", float64(free)/(1<<30))
}
