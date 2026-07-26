package state

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRejectsConcurrentWriterAndReleasesLock(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	if _, err := Open(dir); !errors.Is(err, ErrDataDirLocked) || !strings.Contains(err.Error(), "pid=") {
		t.Fatalf("second writer should be rejected with owner metadata: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, dataDirLockFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode=%o want=600", info.Mode().Perm())
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFailureReleasesLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("invalid state should fail")
	}
	if err := os.Remove(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("failed Open retained data directory lock: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsWriterInAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	command := exec.Command(os.Args[0], "-test.run=^TestDataDirLockHelperProcess$")
	command.Env = append(os.Environ(), "FCP_LOCK_HELPER=1", "FCP_LOCK_DATA_DIR="+dir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper process did not observe the lock: %v\n%s", err, output)
	}
}

func TestDataDirLockHelperProcess(t *testing.T) {
	if os.Getenv("FCP_LOCK_HELPER") != "1" {
		return
	}
	store, err := Open(os.Getenv("FCP_LOCK_DATA_DIR"))
	if store != nil {
		_ = store.Close()
	}
	if !errors.Is(err, ErrDataDirLocked) {
		t.Fatalf("Open error=%v want=%v", err, ErrDataDirLocked)
	}
}
