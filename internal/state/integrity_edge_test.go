package state

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistentStateAndDirectoryIntegrityHelpers(t *testing.T) {
	root := t.TempDir()
	if _, err := readPersistentStateFile(root); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("directory state file error=%v", err)
	}
	file := filepath.Join(root, "state.json")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, err := readPersistentStateFile(file); err != nil || string(raw) != "{}" {
		t.Fatalf("state file read failed: raw=%q err=%v", raw, err)
	}
	if err := requireRegularDirectory(root, "root"); err != nil {
		t.Fatal(err)
	}
	if err := requireRegularDirectory(file, "file"); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("regular file accepted as directory: %v", err)
	}
	link := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(link)
	if err := requireRegularDirectory(link, "link"); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("symlink accepted as directory: %v", err)
	}
}

func TestSnapshotStructureAndRollbackHelpers(t *testing.T) {
	valid := emptySnapshot()
	if err := validateSnapshotStructure(valid); err != nil {
		t.Fatalf("empty normalized snapshot should be valid: %v", err)
	}
	tests := []func(*snapshot){
		func(data *snapshot) { data.Buckets["bad"] = nil },
		func(data *snapshot) { data.MultipartUploads["bad"] = nil },
		func(data *snapshot) { data.Queues["bad"] = nil },
		func(data *snapshot) { data.DynamoTables["bad"] = nil },
		func(data *snapshot) { data.GCSBuckets["bad"] = nil },
		func(data *snapshot) { data.PubSubTopics["bad"] = nil },
		func(data *snapshot) { data.PubSubSubscriptions["bad"] = nil },
		func(data *snapshot) { data.FirestoreDocuments["bad"] = nil },
		func(data *snapshot) { data.Secrets["bad"] = nil },
		func(data *snapshot) { data.KMSKeyRings["bad"] = nil },
		func(data *snapshot) { data.KMSCryptoKeys["bad"] = nil },
		func(data *snapshot) { data.IAMServiceAccounts["bad"] = nil },
	}
	for index, mutate := range tests {
		data := emptySnapshot()
		mutate(&data)
		if err := validateSnapshotStructure(data); err == nil {
			t.Fatalf("nil pointer snapshot case %d should fail", index)
		}
	}
	if got := pointerMap(map[string]*Bucket{"nil": nil}); got["nil"] != nil {
		t.Fatalf("nil pointer map value changed: %+v", got)
	}

	saveFailure := errors.New("save failed")
	store := &Store{}
	if err := store.rollbackSaveLocked(saveFailure); !errors.Is(err, saveFailure) {
		t.Fatalf("empty rollback changed save error: %v", err)
	}
	store.committedRaw = []byte("{")
	if err := store.rollbackSaveLocked(saveFailure); !errors.Is(err, saveFailure) || err.Error() == saveFailure.Error() {
		t.Fatalf("invalid committed state should join rollback error: %v", err)
	}
	store.data = emptySnapshot()
	if err := store.initializeCommittedState(); err != nil || len(store.committedRaw) == 0 {
		t.Fatalf("committed state initialization failed: %v", err)
	}
}

func TestObjectInspectionReadAndDigestHelpers(t *testing.T) {
	root := t.TempDir()
	body := []byte("object")
	file := filepath.Join(root, "object")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	integrity, err := inspectObjectFile(file, "object", int64(len(body)))
	if err != nil || integrity.size != int64(len(body)) || integrity.md5 == "" || integrity.sha256 == "" || integrity.crc32c == "" {
		t.Fatalf("object inspection failed: %+v err=%v", integrity, err)
	}
	if _, err := inspectObjectFile(filepath.Join(root, "missing"), "missing", 0); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("missing object inspection error=%v", err)
	}
	if _, err := inspectObjectFile(file, "object", 1); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("wrong-size object inspection error=%v", err)
	}
	if _, err := inspectObjectFile(root, "directory", 0); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("directory object inspection error=%v", err)
	}

	store := &Store{objects: root, integrityMode: IntegrityModeStrict}
	if _, err := store.openObjectFileForRead("../unsafe", 0); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("unsafe strict object path error=%v", err)
	}
	if _, err := store.openObjectFileForRead("missing", 0); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("missing strict object error=%v", err)
	}
	if _, err := store.openObjectFileForRead("object", 1); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("wrong-size strict object error=%v", err)
	}
	opened, err := store.openObjectFileForRead("object", int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	_ = opened.Close()
	read, err := store.readObjectBody("object", int64(len(body)), integrity.sha256)
	if err != nil || string(read) != string(body) {
		t.Fatalf("strict object read failed: body=%q err=%v", read, err)
	}
	if _, err := store.readObjectBody("object", int64(len(body)), "wrong"); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("strict checksum mismatch error=%v", err)
	}
	store.integrityMode = IntegrityModeStartup
	if read, err := store.readObjectBody("object", 0, ""); err != nil || string(read) != string(body) {
		t.Fatalf("startup-mode object read failed: body=%q err=%v", read, err)
	}

	if err := requireMatchingSHA256("object", "", integrity.sha256); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("missing SHA-256 error=%v", err)
	}
	if err := requireMatchingSHA256("object", "wrong", integrity.sha256); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("mismatched SHA-256 error=%v", err)
	}
	if err := requireMatchingSHA256("object", integrity.sha256, integrity.sha256); err != nil {
		t.Fatal(err)
	}

	sum := md5.Sum(body)
	hexDigest := hex.EncodeToString(sum[:])
	base64Digest := base64.StdEncoding.EncodeToString(sum[:])
	if !equalMD5(hexDigest, hexDigest) || !equalMD5(hexDigest, `"`+hexDigest+`"`) || !equalMD5(hexDigest, base64Digest) || equalMD5(hexDigest, "invalid") {
		t.Fatal("MD5 encoding comparison is incorrect")
	}
}

func TestLockOwnerFormattingAndNilRelease(t *testing.T) {
	if err := releaseDataDirLock(nil); err != nil {
		t.Fatalf("nil lock release failed: %v", err)
	}
	file, err := os.CreateTemp(t.TempDir(), "lock-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(`{"pid":42,"startedAt":"2026-07-27T00:00:00Z"}`); err != nil {
		t.Fatal(err)
	}
	if got := readDataDirLockOwner(file); got != "pid=42 started=2026-07-27T00:00:00Z" {
		t.Fatalf("unexpected lock owner: %q", got)
	}
	if err := file.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"pid":0,"startedAt":"` + time.Now().UTC().Format(time.RFC3339) + `"}`); err != nil {
		t.Fatal(err)
	}
	if got := readDataDirLockOwner(file); got != "" {
		t.Fatalf("invalid lock owner should be hidden: %q", got)
	}
}
