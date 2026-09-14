package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"gorm.io/gorm"
)

var immichCacheFilesMu sync.Mutex

var removeStagedCacheFilesFn = removeStagedCacheFiles

type stagedCacheFile struct{ original, staged string }

func stageCacheFiles(paths []string) ([]stagedCacheFile, error) {
	staged := make([]stagedCacheFile, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			_ = restoreCacheFiles(staged)
			return nil, err
		}
		if !info.Mode().IsRegular() {
			_ = restoreCacheFiles(staged)
			return nil, fmt.Errorf("cache path is not a regular file: %s", path)
		}
		target := path + ".deleting"
		if err := os.Rename(path, target); err != nil {
			_ = restoreCacheFiles(staged)
			return nil, err
		}
		staged = append(staged, stagedCacheFile{original: path, staged: target})
	}
	return staged, nil
}

func restoreCacheFiles(files []stagedCacheFile) error {
	var first error
	for i := len(files) - 1; i >= 0; i-- {
		if err := os.Rename(files[i].staged, files[i].original); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	return first
}

func removeStagedCacheFiles(files []stagedCacheFile) error {
	var first error
	for _, file := range files {
		if err := os.Remove(file.staged); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	return first
}

func recoverStagedCacheFiles(db *gorm.DB, dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*.deleting"))
	if err != nil {
		return fmt.Errorf("scan cache tombstones: %w", err)
	}
	for _, staged := range matches {
		original := strings.TrimSuffix(staged, ".deleting")
		var live int64
		if err := db.Model(&model.ImmichCache{}).Where("file_path = ?", original).Count(&live).Error; err != nil {
			return fmt.Errorf("check cache tombstone owner: %w", err)
		}
		if live > 0 {
			if _, err := os.Stat(original); err == nil {
				if err := os.Remove(staged); err != nil {
					return fmt.Errorf("remove superseded cache tombstone: %w", err)
				}
			} else if os.IsNotExist(err) {
				if err := os.Rename(staged, original); err != nil {
					return fmt.Errorf("restore cache tombstone: %w", err)
				}
			} else {
				return fmt.Errorf("inspect cache path: %w", err)
			}
		} else if err := os.Remove(staged); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove orphan cache tombstone: %w", err)
		}
	}
	return nil
}
