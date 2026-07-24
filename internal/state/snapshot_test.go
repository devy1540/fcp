package state

import (
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
