package util

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CrashLogPrefix marks crash logs saved on graceful shutdown. Files with this basename prefix
// are never auto-cleaned by PurgeFilesInDirectory.
const CrashLogPrefix = "crash"

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
		if strings.HasPrefix(fi.Name(), CrashLogPrefix) {
			// crash logs are preserved unconditionally
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

// CopyFile copies the contents of src into a freshly created dst file.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func AppendLineToFile(filename string, format string, args ...any) {
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf(">>>>>>>>>>>>>>>>>>>>>>>>>>>>> Error opening file: %v", err)
		return
	}
	defer MustClose(f)

	_, _ = fmt.Fprintf(f, format+"\n", args...)
}

func MustClose(f io.Closer) {
	AssertNoError(f.Close())
}
