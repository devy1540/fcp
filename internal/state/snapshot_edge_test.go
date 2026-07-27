package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotDirectoryAndTargetHelpers(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if exists, err := directoryExists(missing); err != nil || exists {
		t.Fatalf("missing directory result: exists=%v err=%v", exists, err)
	}
	if err := requireDirectory(missing, "missing"); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("missing required directory error=%v", err)
	}
	if err := removeDirectoryIfPresent(missing); err != nil {
		t.Fatalf("remove missing directory: %v", err)
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if exists, err := directoryExists(directory); err != nil || !exists {
		t.Fatalf("existing directory result: exists=%v err=%v", exists, err)
	}
	if err := requireDirectory(directory, "directory"); err != nil {
		t.Fatal(err)
	}
	if err := removeDirectoryIfPresent(directory); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := directoryExists(file); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("regular file should not be a directory: %v", err)
	}
	symlink := filepath.Join(root, "link")
	if err := os.Symlink(root, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := directoryExists(symlink); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("symlink should not be a directory: %v", err)
	}

	if err := ensureEmptySnapshotTarget(""); err == nil {
		t.Fatal("empty target path should fail")
	}
	target := filepath.Join(root, "target")
	if err := ensureEmptySnapshotTarget(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "dirty"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureEmptySnapshotTarget(target); !errors.Is(err, ErrSnapshotTargetDirty) {
		t.Fatalf("dirty target error=%v", err)
	}
	snapshotRootFile := filepath.Join(root, "data", "snapshots")
	if err := os.MkdirAll(filepath.Dir(snapshotRootFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotRootFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureSnapshotRoot(filepath.Dir(snapshotRootFile)); err == nil {
		t.Fatal("snapshot root file should fail")
	}
}

func TestSnapshotFileIntegrityHelpers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	body := []byte("snapshot")
	file, err := writeSnapshotBytes(path, "state.json", body)
	if err != nil {
		t.Fatal(err)
	}
	if file.Size != int64(len(body)) || file.SHA256 == "" {
		t.Fatalf("unexpected snapshot file metadata: %+v", file)
	}
	if _, err := writeSnapshotBytes(path, "state.json", body); err == nil {
		t.Fatal("exclusive snapshot write should reject an existing file")
	}
	read, err := readSnapshotFile(path, file, 1<<20)
	if err != nil || string(read) != string(body) {
		t.Fatalf("snapshot read failed: body=%q err=%v", read, err)
	}
	if err := verifySnapshotFile(filepath.Join(root, "missing"), file, 1<<20); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("missing snapshot file error=%v", err)
	}
	wrongSize := file
	wrongSize.Size++
	if err := verifySnapshotFile(path, wrongSize, 1<<20); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("wrong size error=%v", err)
	}
	if err := verifySnapshotFile(path, file, 1); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("oversized snapshot file error=%v", err)
	}
	wrongHash := file
	wrongHash.SHA256 = strings.Repeat("0", sha256.Size*2)
	if err := verifySnapshotFile(path, wrongHash, 1<<20); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("wrong checksum error=%v", err)
	}
	if err := verifySnapshotFile(root, snapshotFile{Name: "directory", Size: 0}, 1<<20); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("directory snapshot file error=%v", err)
	}

	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	copied, err := copySnapshotFile(source, filepath.Join(root, "destination"), "object")
	if err != nil || copied.Name != "object" || copied.Size != 4 {
		t.Fatalf("snapshot copy failed: copied=%+v err=%v", copied, err)
	}
	if _, err := copySnapshotFile(root, filepath.Join(root, "directory-copy"), "object"); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("directory source error=%v", err)
	}
}

func TestSnapshotManifestReferenceAndListingEdges(t *testing.T) {
	root := t.TempDir()
	if snapshots, err := listSnapshots(root); err != nil || len(snapshots) != 0 {
		t.Fatalf("missing snapshot root should list empty: %+v err=%v", snapshots, err)
	}
	snapshotsRoot := filepath.Join(root, "snapshots")
	if err := os.Mkdir(snapshotsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotsRoot, "regular-file"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(snapshotsRoot, ".invalid"), 0o700); err != nil {
		t.Fatal(err)
	}
	if snapshots, err := listSnapshots(root); err != nil || len(snapshots) != 0 {
		t.Fatalf("invalid entries should be ignored: %+v err=%v", snapshots, err)
	}

	validDirectory := filepath.Join(snapshotsRoot, "valid")
	if err := os.Mkdir(validDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshotManifest(validDirectory, "valid"); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("missing manifest error=%v", err)
	}
	if err := os.WriteFile(filepath.Join(validDirectory, "manifest.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshotManifest(validDirectory, "valid"); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("invalid manifest JSON error=%v", err)
	}
	unsupported := `{"schemaVersion":"wrong","name":"valid","createdAt":"2026-07-27T00:00:00Z","state":{"name":"state.json"}}`
	if err := os.WriteFile(filepath.Join(validDirectory, "manifest.json"), []byte(unsupported), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshotManifest(validDirectory, "valid"); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("unsupported manifest error=%v", err)
	}

	if _, err := readSnapshot(root, "../invalid"); !errors.Is(err, ErrSnapshotInvalidName) {
		t.Fatalf("invalid snapshot name error=%v", err)
	}
	if _, err := readSnapshot(root, "missing"); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("missing snapshot error=%v", err)
	}
	fileSnapshot := filepath.Join(snapshotsRoot, "file")
	if err := os.WriteFile(fileSnapshot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshot(root, "file"); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("snapshot file path error=%v", err)
	}

	objectName := "object-file"
	data := emptySnapshot()
	data.Buckets["assets"] = &Bucket{Name: "assets", Objects: map[string]Object{"one": {File: objectName}}}
	data.MultipartUploads["upload"] = &MultipartUpload{Parts: map[int]MultipartPart{1: {File: objectName}}}
	data.GCSBuckets["gcs"] = &GCSBucket{Name: "gcs", Objects: map[string]GCSObject{"one": {File: "gcs-file"}}}
	files, err := referencedObjectFiles(data)
	if err != nil || len(files) != 2 || files[0] != "gcs-file" || files[1] != objectName {
		t.Fatalf("unexpected referenced files: %+v err=%v", files, err)
	}
	data.Buckets["assets"].Objects["bad"] = Object{File: "../unsafe"}
	if _, err := referencedObjectFiles(data); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("unsafe object reference error=%v", err)
	}
	for _, name := range []string{"", ".", "..", "dir/file", `dir\file`} {
		if validSnapshotObjectName(name) {
			t.Fatalf("unsafe snapshot object name accepted: %q", name)
		}
	}
}

func TestSnapshotDeleteAndPersistedDigestEdges(t *testing.T) {
	store := openEdgeStore(t)
	if err := store.DeleteSnapshot("../invalid"); !errors.Is(err, ErrSnapshotInvalidName) {
		t.Fatalf("invalid delete name error=%v", err)
	}
	if err := store.DeleteSnapshot("missing"); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("missing snapshot delete error=%v", err)
	}
	snapshotPath := filepath.Join(store.dir, "snapshots", "broken")
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSnapshot("broken"); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("non-directory snapshot delete error=%v", err)
	}

	if digest, err := persistedStateDigest(filepath.Join(store.dir, "missing.json")); err != nil || digest != "" {
		t.Fatalf("missing state digest=%q err=%v", digest, err)
	}
	invalid := filepath.Join(store.dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := persistedStateDigest(invalid); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("invalid state digest error=%v", err)
	}
	valid := emptySnapshot()
	raw, err := encodeSnapshot(valid)
	if err != nil {
		t.Fatal(err)
	}
	validPath := filepath.Join(store.dir, "valid.json")
	if err := os.WriteFile(validPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := persistedStateDigest(validPath)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(raw)
	if digest != hex.EncodeToString(expected[:]) {
		t.Fatalf("unexpected persisted state digest: %q", digest)
	}

	manifest := snapshotManifest{
		SchemaVersion:         snapshotSchemaVersion,
		Name:                  "valid",
		CreatedAt:             time.Now(),
		ContainsSensitiveData: false,
		Objects:               []snapshotFile{{Name: "one"}},
		SizeBytes:             10,
	}
	info := manifest.info()
	if info.Name != "valid" || info.ObjectCount != 1 || !info.ContainsSensitiveData {
		t.Fatalf("unexpected public snapshot info: %+v", info)
	}
}

func TestReadSnapshotCorruptionMatrix(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, *snapshotManifest)
	}{
		{
			name: "object count mismatch",
			mutate: func(_ *testing.T, _ string, manifest *snapshotManifest) {
				manifest.Objects = manifest.Objects[:1]
			},
		},
		{
			name: "invalid object filename",
			mutate: func(_ *testing.T, _ string, manifest *snapshotManifest) {
				manifest.Objects[0].Name = "../unsafe"
			},
		},
		{
			name: "duplicate object entry",
			mutate: func(_ *testing.T, _ string, manifest *snapshotManifest) {
				manifest.Objects[1] = manifest.Objects[0]
			},
		},
		{
			name: "unexpected object entry",
			mutate: func(_ *testing.T, _ string, manifest *snapshotManifest) {
				manifest.Objects[0].Name = "unexpected"
			},
		},
		{
			name: "size mismatch",
			mutate: func(_ *testing.T, _ string, manifest *snapshotManifest) {
				manifest.SizeBytes++
			},
		},
		{
			name: "invalid state JSON",
			mutate: func(t *testing.T, directory string, manifest *snapshotManifest) {
				t.Helper()
				rewriteSnapshotState(t, directory, manifest, []byte("{"))
			},
		},
		{
			name: "invalid state structure",
			mutate: func(t *testing.T, directory string, manifest *snapshotManifest) {
				t.Helper()
				data := emptySnapshot()
				data.Buckets["broken"] = nil
				raw, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				rewriteSnapshotState(t, directory, manifest, raw)
			},
		},
		{
			name: "unsafe state object reference",
			mutate: func(t *testing.T, directory string, manifest *snapshotManifest) {
				t.Helper()
				raw, err := os.ReadFile(filepath.Join(directory, "state.json"))
				if err != nil {
					t.Fatal(err)
				}
				var data snapshot
				if err := json.Unmarshal(raw, &data); err != nil {
					t.Fatal(err)
				}
				bucket := data.Buckets["assets"]
				object := bucket.Objects["one"]
				object.File = "../unsafe"
				bucket.Objects["one"] = object
				raw, err = json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				rewriteSnapshotState(t, directory, manifest, raw)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			store, err := Open(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.CreateBucket("assets"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.PutObject("assets", "one", []byte("one"), "", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := store.PutObject("assets", "two", []byte("two"), "", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := store.SaveSnapshot("case"); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(dataDir, "snapshots", "case")
			manifestRaw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest snapshotManifest
			if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, directory, &manifest)
			manifestRaw, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "manifest.json"), manifestRaw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readSnapshot(dataDir, "case"); !errors.Is(err, ErrSnapshotCorrupt) {
				t.Fatalf("corrupt snapshot should fail: %v", err)
			}
		})
	}
}

func rewriteSnapshotState(t *testing.T, directory string, manifest *snapshotManifest, raw []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, "state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	manifest.State.Size = int64(len(raw))
	manifest.State.SHA256 = hex.EncodeToString(sum[:])
}
