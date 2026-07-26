package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const dataDirLockFile = ".fcp.lock"

type dataDirLockOwner struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
}

func acquireDataDirLock(dir string) (*os.File, error) {
	path := filepath.Join(dir, dataDirLockFile)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open data directory lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure data directory lock: %w", err)
	}
	if err := tryLockFile(file); err != nil {
		owner := readDataDirLockOwner(file)
		_ = file.Close()
		if isLockUnavailable(err) {
			if owner != "" {
				return nil, fmt.Errorf("%w: %s (%s)", ErrDataDirLocked, dir, owner)
			}
			return nil, fmt.Errorf("%w: %s", ErrDataDirLocked, dir)
		}
		return nil, fmt.Errorf("lock data directory: %w", err)
	}

	payload, err := json.Marshal(dataDirLockOwner{PID: os.Getpid(), StartedAt: time.Now().UTC()})
	if err != nil {
		_ = releaseDataDirLock(file)
		return nil, err
	}
	payload = append(payload, '\n')
	if err := file.Truncate(0); err != nil {
		_ = releaseDataDirLock(file)
		return nil, fmt.Errorf("truncate data directory lock: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = releaseDataDirLock(file)
		return nil, fmt.Errorf("seek data directory lock: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = releaseDataDirLock(file)
		return nil, fmt.Errorf("write data directory lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = releaseDataDirLock(file)
		return nil, fmt.Errorf("sync data directory lock: %w", err)
	}
	return file, nil
}

func releaseDataDirLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return errors.Join(unlockFile(file), file.Close())
}

func readDataDirLockOwner(file *os.File) string {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4<<10))
	if err != nil {
		return ""
	}
	var owner dataDirLockOwner
	if err := json.Unmarshal(raw, &owner); err != nil || owner.PID <= 0 || owner.StartedAt.IsZero() {
		return ""
	}
	return fmt.Sprintf("pid=%d started=%s", owner.PID, owner.StartedAt.Format(time.RFC3339Nano))
}
