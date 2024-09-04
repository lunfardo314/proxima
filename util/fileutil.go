package util

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
