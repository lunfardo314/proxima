package util

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

func PurgeFilesInDirectory(directory, namePattern string, keepLatest int) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("PurgeAndMaintainLatestFiles: %w", err)
	}

	var fi os.FileInfo
	var matches bool

	entries = PurgeSlice(entries, func(entry os.DirEntry) bool {
		if entry.Type()&os.ModeType != 0 {
			// remain only regular files
			return false
		}
		fi, err = entry.Info()
		if err != nil {
			return false
		}
		if matches, err = filepath.Match(namePattern, fi.Name()); err != nil || !matches {
			return false
		}
		return true
	})

	if len(entries) <= keepLatest {
		return nil
	}

	sort.Slice(entries, func(i, j int) bool {
		fii, _ := entries[i].Info()
		fij, _ := entries[j].Info()
		return fii.ModTime().Before(fij.ModTime())
	})

	for _, entry := range entries[:len(entries)-keepLatest] {
		fpath := filepath.Join(directory, entry.Name())
		_ = os.Remove(fpath) // some may not be possible to remove
	}
	return nil
}

func GetDiskUsage(path string) (total uint64, available uint64, free uint64) {
	var stat syscall.Statfs_t

	// Get file system statistics
	err := syscall.Statfs(path, &stat)
	if err != nil {
		fmt.Println("Error getting disk usage:", err)
		return 0, 0, 0
	}

	// Total available blocks * size per block = total space
	total = stat.Blocks * uint64(stat.Bsize)

	// Available blocks for non-superuser * size per block = free space available to normal users
	available = stat.Bavail * uint64(stat.Bsize)

	// Free blocks * size per block = total free space (including root user)
	free = stat.Bfree * uint64(stat.Bsize)

	return total, available, free
}
