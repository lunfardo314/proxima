package main

import (
	"fmt"

	"github.com/lunfardo314/proxima/util"
)

func main() {
	total, available, free := util.GetDiskUsage("/")
	fmt.Printf("Total space: %.2f GB\n", float64(total)/(1024*1024*1024))
	fmt.Printf("Available space: %.2f GB\n", float64(available)/(1024*1024*1024))
	fmt.Printf("Free space: %.2f GB\n", float64(free)/(1024*1024*1024))
}
