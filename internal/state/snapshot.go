package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const snapshotSchemaVersion = "fcp.snapshot/v1"

var (
	ErrSnapshotInvalidName = errors.New("snapshot name is invalid")
	ErrSnapshotExists      = errors.New("snapshot already exists")
	ErrSnapshotNotFound    = errors.New("snapshot not found")
	ErrSnapshotCorrupt     = errors.New("snapshot is corrupt")
	ErrSnapshotTargetDirty = errors.New("snapshot target directory is not empty")
)

// SnapshotInfo is safe to expose through the CLI and dashboard API. Snapshot
// contents are intentionally excluded because state.json can contain local
// Secret payloads and private key material.
type SnapshotInfo struct {
	SchemaVersion         string    `json:"schemaVersion"`
	Name                  string    `json:"name"`
	CreatedAt             time.Time `json:"createdAt"`
	ObjectCount           int       `json:"objectCount"`
	SizeBytes             int64     `json:"sizeBytes"`
	ContainsSensitiveData bool      `json:"containsSensitiveData"`
}

type snapshotFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type snapshotManifest struct {
	SchemaVersion         string         `json:"schemaVersion"`
	Name                  string         `json:"name"`
	CreatedAt             time.Time      `json:"createdAt"`
	ContainsSensitiveData bool           `json:"containsSensitiveData"`
	State                 snapshotFile   `json:"state"`
	Objects               []snapshotFile `json:"objects"`
	SizeBytes             int64          `json:"sizeBytes"`
}

type loadedSnapshot struct {
	info     SnapshotInfo
	manifest snapshotManifest
	data     snapshot
	stateRaw []byte
	dir      string
}

// SaveSnapshot creates an immutable, checksummed copy of the current state.
// A snapshot is never overwritten implicitly.
func (s *Store) SaveSnapshot(name string) (SnapshotInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !validSnapshotName(name) {
		return SnapshotInfo{}, ErrSnapshotInvalidName
	}
	root, err := ensureSnapshotRoot(s.dir)
	if err != nil {
		return SnapshotInfo{}, err
	}
	destination := filepath.Join(root, name)
	if _, err := os.Lstat(destination); err == nil {
		return SnapshotInfo{}, ErrSnapshotExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return SnapshotInfo{}, err
	}

	temporary, err := os.MkdirTemp(root, ".snapshot-*")
	if err != nil {
		return SnapshotInfo{}, err
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return SnapshotInfo{}, err
	}
	objectsDir := filepath.Join(temporary, "objects")
	if err := os.Mkdir(objectsDir, 0o700); err != nil {
		return SnapshotInfo{}, err
	}

	stateRaw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return SnapshotInfo{}, fmt.Errorf("encode snapshot state: %w", err)
	}
	stateRaw = append(stateRaw, '\n')
	stateFile, err := writeSnapshotBytes(filepath.Join(temporary, "state.json"), "state.json", stateRaw)
	if err != nil {
		return SnapshotInfo{}, err
	}

	objectNames, err := referencedObjectFiles(s.data)
	if err != nil {
		return SnapshotInfo{}, err
	}
	objectFiles := make([]snapshotFile, 0, len(objectNames))
	sizeBytes := stateFile.Size
	for _, objectName := range objectNames {
		file, err := copySnapshotFile(filepath.Join(s.objects, objectName), filepath.Join(objectsDir, objectName), objectName)
		if err != nil {
			return SnapshotInfo{}, fmt.Errorf("snapshot object %s: %w", objectName, err)
		}
		objectFiles = append(objectFiles, file)
		sizeBytes += file.Size
	}

	manifest := snapshotManifest{
		SchemaVersion:         snapshotSchemaVersion,
		Name:                  name,
		CreatedAt:             s.now().UTC(),
		ContainsSensitiveData: true,
		State:                 stateFile,
		Objects:               objectFiles,
		SizeBytes:             sizeBytes,
	}
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return SnapshotInfo{}, fmt.Errorf("encode snapshot manifest: %w", err)
	}
	manifestRaw = append(manifestRaw, '\n')
	if _, err := writeSnapshotBytes(filepath.Join(temporary, "manifest.json"), "manifest.json", manifestRaw); err != nil {
		return SnapshotInfo{}, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		if _, statErr := os.Lstat(destination); statErr == nil {
			return SnapshotInfo{}, ErrSnapshotExists
		}
		return SnapshotInfo{}, err
	}
	return manifest.info(), nil
}

// ListSnapshots returns metadata only and never reads state payloads.
func (s *Store) ListSnapshots() ([]SnapshotInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return listSnapshots(s.dir)
}

// LoadSnapshot replaces the live Store state with an integrity-checked
// snapshot. The previous object directory is retained until state.json has
// been atomically replaced, allowing rollback on failure.
func (s *Store) LoadSnapshot(name string) (SnapshotInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	loaded, err := readSnapshot(s.dir, name)
	if err != nil {
		return SnapshotInfo{}, err
	}
	stage, err := os.MkdirTemp(s.dir, ".snapshot-restore-*")
	if err != nil {
		return SnapshotInfo{}, err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return SnapshotInfo{}, err
	}
	if err := materializeLoadedSnapshot(loaded, stage); err != nil {
		return SnapshotInfo{}, err
	}

	backup, err := os.MkdirTemp(s.dir, ".objects-backup-*")
	if err != nil {
		return SnapshotInfo{}, err
	}
	if err := os.Remove(backup); err != nil {
		return SnapshotInfo{}, err
	}
	if err := os.Rename(s.objects, backup); err != nil {
		return SnapshotInfo{}, err
	}
	if err := os.Rename(filepath.Join(stage, "objects"), s.objects); err != nil {
		_ = os.Rename(backup, s.objects)
		return SnapshotInfo{}, err
	}

	previous := s.data
	s.data = loaded.data
	if err := s.saveLocked(); err != nil {
		s.data = previous
		_ = os.RemoveAll(s.objects)
		_ = os.Rename(backup, s.objects)
		return SnapshotInfo{}, err
	}
	_ = os.RemoveAll(backup)
	return loaded.info, nil
}

func (s *Store) DeleteSnapshot(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !validSnapshotName(name) {
		return ErrSnapshotInvalidName
	}
	destination := filepath.Join(s.dir, "snapshots", name)
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return ErrSnapshotNotFound
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: snapshot path is not a directory", ErrSnapshotCorrupt)
	}
	return os.RemoveAll(destination)
}

// MaterializeSnapshot copies a named snapshot into an empty data directory.
// It is used by `fcp exec` to create an isolated, disposable FCP instance.
func MaterializeSnapshot(dataDir, name, targetDir string) (SnapshotInfo, error) {
	loaded, err := readSnapshot(dataDir, name)
	if err != nil {
		return SnapshotInfo{}, err
	}
	if err := ensureEmptySnapshotTarget(targetDir); err != nil {
		return SnapshotInfo{}, err
	}
	if err := materializeLoadedSnapshot(loaded, targetDir); err != nil {
		return SnapshotInfo{}, err
	}
	return loaded.info, nil
}

func listSnapshots(dataDir string) ([]SnapshotInfo, error) {
	root := filepath.Join(dataDir, "snapshots")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []SnapshotInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]SnapshotInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validSnapshotName(entry.Name()) {
			continue
		}
		manifest, err := readSnapshotManifest(filepath.Join(root, entry.Name()), entry.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, manifest.info())
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].Name < result[j].Name
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

func readSnapshot(dataDir, name string) (loadedSnapshot, error) {
	if !validSnapshotName(name) {
		return loadedSnapshot{}, ErrSnapshotInvalidName
	}
	directory := filepath.Join(dataDir, "snapshots", name)
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return loadedSnapshot{}, ErrSnapshotNotFound
	}
	if err != nil {
		return loadedSnapshot{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return loadedSnapshot{}, fmt.Errorf("%w: snapshot path is not a directory", ErrSnapshotCorrupt)
	}
	manifest, err := readSnapshotManifest(directory, name)
	if err != nil {
		return loadedSnapshot{}, err
	}

	stateRaw, err := readSnapshotFile(filepath.Join(directory, manifest.State.Name), manifest.State, 64<<20)
	if err != nil {
		return loadedSnapshot{}, err
	}
	var data snapshot
	if err := json.Unmarshal(stateRaw, &data); err != nil {
		return loadedSnapshot{}, fmt.Errorf("%w: decode state: %v", ErrSnapshotCorrupt, err)
	}
	normalizeSnapshot(&data)
	referenced, err := referencedObjectFiles(data)
	if err != nil {
		return loadedSnapshot{}, err
	}
	if len(referenced) != len(manifest.Objects) {
		return loadedSnapshot{}, fmt.Errorf("%w: object manifest does not match state", ErrSnapshotCorrupt)
	}
	expected := make(map[string]struct{}, len(referenced))
	for _, name := range referenced {
		expected[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(manifest.Objects))
	for _, file := range manifest.Objects {
		if !validSnapshotObjectName(file.Name) {
			return loadedSnapshot{}, fmt.Errorf("%w: invalid object filename", ErrSnapshotCorrupt)
		}
		if _, duplicate := seen[file.Name]; duplicate {
			return loadedSnapshot{}, fmt.Errorf("%w: duplicate object manifest entry", ErrSnapshotCorrupt)
		}
		seen[file.Name] = struct{}{}
		if _, ok := expected[file.Name]; !ok {
			return loadedSnapshot{}, fmt.Errorf("%w: unexpected object manifest entry", ErrSnapshotCorrupt)
		}
		if err := verifySnapshotFile(filepath.Join(directory, "objects", file.Name), file, 1<<40); err != nil {
			return loadedSnapshot{}, err
		}
	}
	sizeBytes := manifest.State.Size
	for _, file := range manifest.Objects {
		sizeBytes += file.Size
	}
	if manifest.SizeBytes != sizeBytes {
		return loadedSnapshot{}, fmt.Errorf("%w: snapshot size does not match manifest", ErrSnapshotCorrupt)
	}
	return loadedSnapshot{
		info:     manifest.info(),
		manifest: manifest,
		data:     data,
		stateRaw: stateRaw,
		dir:      directory,
	}, nil
}

func readSnapshotManifest(directory, expectedName string) (snapshotManifest, error) {
	raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return snapshotManifest{}, fmt.Errorf("%w: manifest is missing", ErrSnapshotCorrupt)
	}
	if err != nil {
		return snapshotManifest{}, err
	}
	if len(raw) > 1<<20 {
		return snapshotManifest{}, fmt.Errorf("%w: manifest is too large", ErrSnapshotCorrupt)
	}
	var manifest snapshotManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return snapshotManifest{}, fmt.Errorf("%w: decode manifest: %v", ErrSnapshotCorrupt, err)
	}
	if manifest.SchemaVersion != snapshotSchemaVersion || manifest.Name != expectedName || manifest.State.Name != "state.json" || manifest.CreatedAt.IsZero() {
		return snapshotManifest{}, fmt.Errorf("%w: unsupported manifest", ErrSnapshotCorrupt)
	}
	return manifest, nil
}

func materializeLoadedSnapshot(loaded loadedSnapshot, targetDir string) error {
	objectsDir := filepath.Join(targetDir, "objects")
	if err := os.MkdirAll(objectsDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(objectsDir, 0o700); err != nil {
		return err
	}
	if _, err := writeSnapshotBytes(filepath.Join(targetDir, "state.json"), "state.json", loaded.stateRaw); err != nil {
		return err
	}
	for _, file := range loaded.manifest.Objects {
		if _, err := copySnapshotFile(filepath.Join(loaded.dir, "objects", file.Name), filepath.Join(objectsDir, file.Name), file.Name); err != nil {
			return err
		}
	}
	return nil
}

func ensureSnapshotRoot(dataDir string) (string, error) {
	root := filepath.Join(dataDir, "snapshots")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}

func ensureEmptySnapshotTarget(targetDir string) error {
	if strings.TrimSpace(targetDir) == "" {
		return errors.New("snapshot target directory is required")
	}
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(targetDir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return ErrSnapshotTargetDirty
	}
	return nil
}

func referencedObjectFiles(data snapshot) ([]string, error) {
	files := map[string]struct{}{}
	add := func(name string) error {
		if !validSnapshotObjectName(name) {
			return fmt.Errorf("%w: invalid object filename", ErrSnapshotCorrupt)
		}
		files[name] = struct{}{}
		return nil
	}
	for _, bucket := range data.Buckets {
		for _, object := range bucket.Objects {
			if err := add(object.File); err != nil {
				return nil, err
			}
		}
	}
	for _, upload := range data.MultipartUploads {
		for _, part := range upload.Parts {
			if err := add(part.File); err != nil {
				return nil, err
			}
		}
	}
	for _, bucket := range data.GCSBuckets {
		for _, object := range bucket.Objects {
			if err := add(object.File); err != nil {
				return nil, err
			}
		}
	}
	result := make([]string, 0, len(files))
	for file := range files {
		result = append(result, file)
	}
	sort.Strings(result)
	return result, nil
}

func copySnapshotFile(source, destination, name string) (snapshotFile, error) {
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return snapshotFile{}, err
	}
	if !sourceInfo.Mode().IsRegular() {
		return snapshotFile{}, fmt.Errorf("%w: source is not a regular file", ErrSnapshotCorrupt)
	}
	input, err := os.Open(source)
	if err != nil {
		return snapshotFile{}, err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return snapshotFile{}, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return snapshotFile{}, copyErr
	}
	if syncErr != nil {
		return snapshotFile{}, syncErr
	}
	if closeErr != nil {
		return snapshotFile{}, closeErr
	}
	return snapshotFile{Name: name, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func writeSnapshotBytes(path, name string, body []byte) (snapshotFile, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return snapshotFile{}, err
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return snapshotFile{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return snapshotFile{}, err
	}
	if err := file.Close(); err != nil {
		return snapshotFile{}, err
	}
	sum := sha256.Sum256(body)
	return snapshotFile{Name: name, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}, nil
}

func readSnapshotFile(path string, expected snapshotFile, limit int64) ([]byte, error) {
	if err := verifySnapshotFile(path, expected, limit); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func verifySnapshotFile(path string, expected snapshotFile, limit int64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: %s is missing", ErrSnapshotCorrupt, expected.Name)
	}
	if !info.Mode().IsRegular() || info.Size() != expected.Size || info.Size() > limit {
		return fmt.Errorf("%w: invalid file metadata for %s", ErrSnapshotCorrupt, expected.Name)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expected.SHA256) {
		return fmt.Errorf("%w: checksum mismatch for %s", ErrSnapshotCorrupt, expected.Name)
	}
	return nil
}

func validSnapshotName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for index, character := range name {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		if index > 0 && (character == '-' || character == '_' || character == '.') {
			continue
		}
		return false
	}
	return true
}

func validSnapshotObjectName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, `/\`)
}

func (manifest snapshotManifest) info() SnapshotInfo {
	return SnapshotInfo{
		SchemaVersion:         manifest.SchemaVersion,
		Name:                  manifest.Name,
		CreatedAt:             manifest.CreatedAt,
		ObjectCount:           len(manifest.Objects),
		SizeBytes:             manifest.SizeBytes,
		ContainsSensitiveData: true,
	}
}
