package main

import (
	"fmt"
	"syscall"
)

func getDiskUsage(path string) (uint64, uint64, uint64) {
	var stat syscall.Statfs_t

	// Get file system statistics
	err := syscall.Statfs(path, &stat)
	if err != nil {
		fmt.Println("Error getting disk usage:", err)
		return 0, 0, 0
	}

	// Total available blocks * size per block = total space
	total := stat.Blocks * uint64(stat.Bsize)

	// Available blocks for non-superuser * size per block = free space available to normal users
	available := stat.Bavail * uint64(stat.Bsize)

	// Free blocks * size per block = total free space (including root user)
	free := stat.Bfree * uint64(stat.Bsize)

	return total, available, free
}

func main() {
	total, available, free := getDiskUsage("/")
	fmt.Printf("Total space: %.2f GB\n", float64(total)/(1024*1024*1024))
	fmt.Printf("Available space: %.2f GB\n", float64(available)/(1024*1024*1024))
	fmt.Printf("Free space: %.2f GB\n", float64(free)/(1024*1024*1024))
}
