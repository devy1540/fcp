package state

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type objectFileIntegrity struct {
	size   int64
	md5    string
	sha256 string
	crc32c string
}

const maxPersistentStateSize = 256 << 20

func readPersistentStateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: state.json is not a regular file", ErrStateCorrupt)
	}
	if info.Size() > maxPersistentStateSize {
		return nil, fmt.Errorf("%w: state.json exceeds %d bytes", ErrStateCorrupt, maxPersistentStateSize)
	}
	return os.ReadFile(path)
}

func requireRegularDirectory(path, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is not a regular directory", ErrStateCorrupt, label)
	}
	return nil
}

func validateSnapshotStructure(data snapshot) error {
	checks := []struct {
		kind   string
		values map[string]any
	}{
		{"bucket", pointerMap(data.Buckets)},
		{"multipart upload", pointerMap(data.MultipartUploads)},
		{"queue", pointerMap(data.Queues)},
		{"DynamoDB table", pointerMap(data.DynamoTables)},
		{"GCS bucket", pointerMap(data.GCSBuckets)},
		{"Pub/Sub topic", pointerMap(data.PubSubTopics)},
		{"Pub/Sub subscription", pointerMap(data.PubSubSubscriptions)},
		{"Firestore document", pointerMap(data.FirestoreDocuments)},
		{"secret", pointerMap(data.Secrets)},
		{"KMS key ring", pointerMap(data.KMSKeyRings)},
		{"KMS crypto key", pointerMap(data.KMSCryptoKeys)},
		{"IAM service account", pointerMap(data.IAMServiceAccounts)},
	}
	for _, candidate := range checks {
		for name, value := range candidate.values {
			if value == nil {
				return fmt.Errorf("%s %q is null", candidate.kind, name)
			}
		}
	}
	return nil
}

func pointerMap[T any](values map[string]*T) map[string]any {
	result := make(map[string]any, len(values))
	for name, value := range values {
		if value == nil {
			result[name] = nil
		} else {
			result[name] = value
		}
	}
	return result
}

func encodeSnapshot(data snapshot) ([]byte, error) {
	return json.MarshalIndent(data, "", "  ")
}

func (s *Store) initializeCommittedState() error {
	raw, err := encodeSnapshot(s.data)
	if err != nil {
		return err
	}
	s.committedRaw = raw
	return nil
}

func (s *Store) rollbackSaveLocked(saveErr error) error {
	if len(s.committedRaw) == 0 {
		return saveErr
	}
	var committed snapshot
	if err := json.Unmarshal(s.committedRaw, &committed); err != nil {
		return errors.Join(saveErr, fmt.Errorf("restore committed state: %w", err))
	}
	normalizeSnapshot(&committed)
	s.data = committed
	return saveErr
}

func generationObjectFile(namespace, owner, key string, contentHash [sha256.Size]byte) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, namespace)
	_, _ = hash.Write([]byte{0})
	_, _ = io.WriteString(hash, owner)
	_, _ = hash.Write([]byte{0})
	_, _ = io.WriteString(hash, key)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(contentHash[:])
	return hex.EncodeToString(hash.Sum(nil))
}

func validateAndCleanupObjectState(objectsDir string, data snapshot) (bool, error) {
	referenced := make(map[string]struct{})
	cache := make(map[string]objectFileIntegrity)
	upgraded := false
	check := func(file string, size int64, expectedSHA256, expectedMD5, expectedCRC32C string) error {
		if !validSnapshotObjectName(file) {
			return fmt.Errorf("%w: invalid object filename %q", ErrStateCorrupt, file)
		}
		referenced[file] = struct{}{}
		integrity, ok := cache[file]
		if !ok {
			var err error
			integrity, err = inspectObjectFile(filepath.Join(objectsDir, file), file, size)
			if err != nil {
				return err
			}
			cache[file] = integrity
		} else if integrity.size != size {
			return fmt.Errorf("%w: conflicting size for object file %s", ErrStateCorrupt, file)
		}
		if expectedSHA256 != "" && !strings.EqualFold(integrity.sha256, expectedSHA256) {
			return fmt.Errorf("%w: SHA-256 mismatch for object file %s", ErrStateCorrupt, file)
		}
		if expectedSHA256 == "" && expectedMD5 != "" && !equalMD5(integrity.md5, expectedMD5) {
			return fmt.Errorf("%w: MD5 mismatch for object file %s", ErrStateCorrupt, file)
		}
		if expectedCRC32C != "" && integrity.crc32c != expectedCRC32C {
			return fmt.Errorf("%w: CRC32C mismatch for object file %s", ErrStateCorrupt, file)
		}
		return nil
	}

	for _, bucket := range data.Buckets {
		for key, object := range bucket.Objects {
			expectedMD5 := ""
			if !strings.Contains(object.ETag, "-") {
				expectedMD5 = object.ETag
			}
			if err := check(object.File, object.Size, object.SHA256, expectedMD5, ""); err != nil {
				return false, err
			}
			if object.SHA256 == "" {
				object.SHA256 = cache[object.File].sha256
				bucket.Objects[key] = object
				upgraded = true
			}
		}
	}
	for _, upload := range data.MultipartUploads {
		for partNumber, part := range upload.Parts {
			if err := check(part.File, part.Size, part.SHA256, part.ETag, ""); err != nil {
				return false, err
			}
			if part.SHA256 == "" {
				part.SHA256 = cache[part.File].sha256
				upload.Parts[partNumber] = part
				upgraded = true
			}
		}
	}
	for _, bucket := range data.GCSBuckets {
		for key, object := range bucket.Objects {
			if err := check(object.File, object.Size, object.SHA256, object.MD5Hash, object.CRC32C); err != nil {
				return false, err
			}
			if object.SHA256 == "" {
				object.SHA256 = cache[object.File].sha256
				bucket.Objects[key] = object
				upgraded = true
			}
		}
	}

	entries, err := os.ReadDir(objectsDir)
	if err != nil {
		return false, err
	}
	removed := false
	for _, entry := range entries {
		if _, ok := referenced[entry.Name()]; ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("%w: unexpected entry in object directory %s", ErrStateCorrupt, entry.Name())
		}
		if err := os.Remove(filepath.Join(objectsDir, entry.Name())); err != nil {
			return false, err
		}
		removed = true
	}
	if removed {
		if err := syncDirectory(objectsDir); err != nil {
			return false, err
		}
	}
	return upgraded, nil
}

func inspectObjectFile(path, name string, expectedSize int64) (objectFileIntegrity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return objectFileIntegrity{}, fmt.Errorf("%w: object file %s is missing", ErrStateCorrupt, name)
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return objectFileIntegrity{}, fmt.Errorf("%w: invalid metadata for object file %s", ErrStateCorrupt, name)
	}
	file, err := os.Open(path)
	if err != nil {
		return objectFileIntegrity{}, fmt.Errorf("%w: open object file %s: %v", ErrStateCorrupt, name, err)
	}
	defer file.Close()
	md5Hash := md5.New()
	sha256Hash := sha256.New()
	crc32cHash := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	if _, err := io.Copy(io.MultiWriter(md5Hash, sha256Hash, crc32cHash), file); err != nil {
		return objectFileIntegrity{}, fmt.Errorf("%w: read object file %s: %v", ErrStateCorrupt, name, err)
	}
	crcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBytes, crc32cHash.Sum32())
	return objectFileIntegrity{
		size:   info.Size(),
		md5:    hex.EncodeToString(md5Hash.Sum(nil)),
		sha256: hex.EncodeToString(sha256Hash.Sum(nil)),
		crc32c: base64.StdEncoding.EncodeToString(crcBytes),
	}, nil
}

func (s *Store) openObjectFileForRead(name string, expectedSize int64) (*os.File, error) {
	path := filepath.Join(s.objects, name)
	if s.integrityMode != IntegrityModeStrict {
		return os.Open(path)
	}
	if !validSnapshotObjectName(name) {
		return nil, fmt.Errorf("%w: invalid object filename %q", ErrStateCorrupt, name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: object file %s is missing", ErrStateCorrupt, name)
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return nil, fmt.Errorf("%w: invalid metadata for object file %s", ErrStateCorrupt, name)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open object file %s: %v", ErrStateCorrupt, name, err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: stat object file %s: %v", ErrStateCorrupt, name, err)
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Size() != expectedSize {
		_ = file.Close()
		return nil, fmt.Errorf("%w: object file %s changed while opening", ErrStateCorrupt, name)
	}
	return file, nil
}

func (s *Store) readObjectBody(name string, expectedSize int64, expectedSHA256 string) ([]byte, error) {
	if s.integrityMode != IntegrityModeStrict {
		return os.ReadFile(filepath.Join(s.objects, name))
	}
	file, err := s.openObjectFileForRead(name, expectedSize)
	if err != nil {
		return nil, err
	}
	body := make([]byte, expectedSize)
	_, readErr := io.ReadFull(file, body)
	if errors.Is(readErr, io.ErrUnexpectedEOF) {
		readErr = fmt.Errorf("%w: object file %s changed while reading", ErrStateCorrupt, name)
	}
	if readErr == nil {
		var extra [1]byte
		if count, extraErr := file.Read(extra[:]); count != 0 || !errors.Is(extraErr, io.EOF) {
			if extraErr != nil && !errors.Is(extraErr, io.EOF) {
				readErr = extraErr
			} else {
				readErr = fmt.Errorf("%w: object file %s changed while reading", ErrStateCorrupt, name)
			}
		}
	}
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(body)) != expectedSize {
		return nil, fmt.Errorf("%w: object file %s changed while reading", ErrStateCorrupt, name)
	}
	sum := sha256.Sum256(body)
	if err := requireMatchingSHA256(name, expectedSHA256, hex.EncodeToString(sum[:])); err != nil {
		return nil, err
	}
	return body, nil
}

func requireMatchingSHA256(name, expected, actual string) error {
	if expected == "" {
		return fmt.Errorf("%w: SHA-256 metadata is missing for object file %s", ErrStateCorrupt, name)
	}
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("%w: SHA-256 mismatch for object file %s", ErrStateCorrupt, name)
	}
	return nil
}

func equalMD5(actualHex, expected string) bool {
	expected = strings.Trim(expected, "\"")
	if decoded, err := base64.StdEncoding.DecodeString(expected); err == nil && len(decoded) == md5.Size {
		return strings.EqualFold(actualHex, hex.EncodeToString(decoded))
	}
	decoded, err := hex.DecodeString(expected)
	return err == nil && len(decoded) == md5.Size && strings.EqualFold(actualHex, hex.EncodeToString(decoded))
}
