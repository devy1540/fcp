package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotSaveLoadMaterializeAndDelete(t *testing.T) {
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("assets", "hello.txt", []byte("snapshot-body"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateQueue("jobs", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SendMessage("jobs", "snapshot-message", nil, 0); err != nil {
		t.Fatal(err)
	}
	secretName := "projects/test/secrets/local"
	if _, err := store.CreateSecret(secretName, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSecretVersion(secretName, []byte("local-secret")); err != nil {
		t.Fatal(err)
	}

	saved, err := store.SaveSnapshot("baseline")
	if err != nil {
		t.Fatal(err)
	}
	if saved.SchemaVersion != snapshotSchemaVersion || saved.ObjectCount != 1 || saved.SizeBytes == 0 || !saved.ContainsSensitiveData {
		t.Fatalf("unexpected snapshot metadata: %+v", saved)
	}
	if _, err := store.SaveSnapshot("baseline"); !errors.Is(err, ErrSnapshotExists) {
		t.Fatalf("duplicate snapshot should fail: %v", err)
	}
	assertSnapshotPermissions(t, dataDir, "baseline")

	if err := store.DeleteObject("assets", "hello.txt"); err != nil {
		t.Fatal(err)
	}
	if err := store.PurgeQueue("jobs"); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadSnapshot("baseline")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "baseline" {
		t.Fatalf("unexpected loaded snapshot: %+v", loaded)
	}
	assertSnapshotData(t, store, secretName)

	materializedDir := t.TempDir()
	materialized, err := MaterializeSnapshot(dataDir, "baseline", materializedDir)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.Name != "baseline" {
		t.Fatalf("unexpected materialized snapshot: %+v", materialized)
	}
	reopened, err := Open(materializedDir)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotData(t, reopened, secretName)

	snapshots, err := store.ListSnapshots()
	if err != nil || len(snapshots) != 1 || snapshots[0].Name != "baseline" {
		t.Fatalf("unexpected snapshot list: snapshots=%+v err=%v", snapshots, err)
	}
	if err := store.DeleteSnapshot("baseline"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSnapshot("baseline"); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("deleted snapshot should be missing: %v", err)
	}
}

func TestSnapshotRejectsInvalidNamesDirtyTargetsAndCorruption(t *testing.T) {
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("assets", "hello.txt", []byte("original"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".hidden", "../escape", "with/slash", "white space"} {
		if _, err := store.SaveSnapshot(name); !errors.Is(err, ErrSnapshotInvalidName) {
			t.Fatalf("snapshot name %q should fail: %v", name, err)
		}
	}
	if _, err := store.SaveSnapshot("baseline"); err != nil {
		t.Fatal(err)
	}

	dirtyTarget := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirtyTarget, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeSnapshot(dataDir, "baseline", dirtyTarget); !errors.Is(err, ErrSnapshotTargetDirty) {
		t.Fatalf("dirty target should fail: %v", err)
	}

	manifestPath := filepath.Join(dataDir, "snapshots", "baseline", "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest snapshotManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Objects) != 1 {
		t.Fatalf("expected one object in manifest: %+v", manifest)
	}
	objectPath := filepath.Join(dataDir, "snapshots", "baseline", "objects", manifest.Objects[0].Name)
	if err := os.WriteFile(objectPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSnapshot("baseline"); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("tampered snapshot should fail: %v", err)
	}
}

func TestSnapshotRestoreJournalRecoversEveryCrashPoint(t *testing.T) {
	for _, phase := range []string{"journal-written", "backup-renamed", "objects-swapped", "state-committed"} {
		t.Run(phase, func(t *testing.T) {
			dataDir := prepareSnapshotRestoreCrash(t, phase)
			store, err := Open(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, body, err := store.GetObject("assets", "hello.txt")
			if err != nil {
				t.Fatal(err)
			}
			want := "old"
			if phase == "state-committed" {
				want = "new"
			}
			if string(body) != want {
				t.Fatalf("recovered body=%q want=%q", body, want)
			}
			for _, path := range []string{
				filepath.Join(dataDir, snapshotRestoreJournalFile),
				filepath.Join(dataDir, ".objects-backup-crash-test"),
				filepath.Join(dataDir, ".snapshot-restore-crash-test"),
			} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("recovery artifact still exists at %s: %v", path, err)
				}
			}
		})
	}
}

func TestSnapshotRestoreJournalRecoversIdenticalStateAfterBackupRename(t *testing.T) {
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("assets", "hello.txt", []byte("same"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveSnapshot("identical"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	loaded, err := readSnapshot(dataDir, "identical")
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(dataDir, ".snapshot-restore-identical")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := materializeLoadedSnapshot(loaded, stage); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dataDir, ".objects-backup-identical")
	targetRaw, err := encodeSnapshot(loaded.data)
	if err != nil {
		t.Fatal(err)
	}
	targetHash := sha256.Sum256(targetRaw)
	if err := writeSnapshotRestoreJournal(dataDir, snapshotRestoreJournal{
		SchemaVersion:     snapshotRestoreJournalVersion,
		BackupDirectory:   filepath.Base(backup),
		StageDirectory:    filepath.Base(stage),
		TargetStateSHA256: hex.EncodeToString(targetHash[:]),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dataDir, "objects"), backup); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, body, err := reopened.GetObject("assets", "hello.txt")
	if err != nil || string(body) != "same" {
		t.Fatalf("identical-state recovery failed: body=%q err=%v", body, err)
	}
	for _, path := range []string{
		filepath.Join(dataDir, snapshotRestoreJournalFile),
		backup,
		stage,
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recovery artifact still exists at %s: %v", path, err)
		}
	}
}

func prepareSnapshotRestoreCrash(t *testing.T, phase string) string {
	t.Helper()
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("assets", "hello.txt", []byte("old"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("assets", "hello.txt", []byte("new"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveSnapshot("target"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("assets", "hello.txt", []byte("old"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	loaded, err := readSnapshot(dataDir, "target")
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(dataDir, ".snapshot-restore-crash-test")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := materializeLoadedSnapshot(loaded, stage); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dataDir, ".objects-backup-crash-test")
	targetRaw, err := encodeSnapshot(loaded.data)
	if err != nil {
		t.Fatal(err)
	}
	targetHash := sha256.Sum256(targetRaw)
	journal := snapshotRestoreJournal{
		SchemaVersion:     snapshotRestoreJournalVersion,
		BackupDirectory:   filepath.Base(backup),
		StageDirectory:    filepath.Base(stage),
		TargetStateSHA256: hex.EncodeToString(targetHash[:]),
	}
	if err := writeSnapshotRestoreJournal(dataDir, journal); err != nil {
		t.Fatal(err)
	}
	if phase == "journal-written" {
		return dataDir
	}
	if err := os.Rename(filepath.Join(dataDir, "objects"), backup); err != nil {
		t.Fatal(err)
	}
	if phase == "backup-renamed" {
		return dataDir
	}
	if err := os.Rename(filepath.Join(stage, "objects"), filepath.Join(dataDir, "objects")); err != nil {
		t.Fatal(err)
	}
	if phase == "objects-swapped" {
		return dataDir
	}
	if phase != "state-committed" {
		t.Fatalf("unknown crash phase %q", phase)
	}
	temporary := filepath.Join(dataDir, ".state-crash-test")
	if err := os.WriteFile(temporary, targetRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, filepath.Join(dataDir, "state.json")); err != nil {
		t.Fatal(err)
	}
	return dataDir
}

func assertSnapshotData(t *testing.T, store *Store, secretName string) {
	t.Helper()
	_, body, err := store.GetObject("assets", "hello.txt")
	if err != nil || string(body) != "snapshot-body" {
		t.Fatalf("unexpected restored object: body=%q err=%v", body, err)
	}
	queue, err := store.Queue("jobs")
	if err != nil || len(queue.Messages) != 1 || queue.Messages[0].Body != "snapshot-message" {
		t.Fatalf("unexpected restored queue: queue=%+v err=%v", queue, err)
	}
	version, err := store.SecretVersion(secretName, 1)
	if err != nil || string(version.Payload) != "local-secret" {
		t.Fatalf("unexpected restored secret: version=%+v err=%v", version, err)
	}
}

func assertSnapshotPermissions(t *testing.T, dataDir, name string) {
	t.Helper()
	for _, check := range []struct {
		path string
		mode os.FileMode
	}{
		{filepath.Join(dataDir, "snapshots"), 0o700},
		{filepath.Join(dataDir, "snapshots", name), 0o700},
		{filepath.Join(dataDir, "snapshots", name, "objects"), 0o700},
		{filepath.Join(dataDir, "snapshots", name, "manifest.json"), 0o600},
		{filepath.Join(dataDir, "snapshots", name, "state.json"), 0o600},
	} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatalf("stat %s: %v", check.path, err)
		}
		if info.Mode().Perm() != check.mode {
			t.Fatalf("%s mode=%o want=%o", check.path, info.Mode().Perm(), check.mode)
		}
	}
}
