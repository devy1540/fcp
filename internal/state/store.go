package state

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultAccountID = "000000000000"

var (
	ErrBucketNotFound          = errors.New("bucket not found")
	ErrBucketNotEmpty          = errors.New("bucket not empty")
	ErrObjectNotFound          = errors.New("object not found")
	ErrMultipartUploadNotFound = errors.New("multipart upload not found")
	ErrInvalidPart             = errors.New("invalid multipart part")
	ErrInvalidPartOrder        = errors.New("multipart parts are not in ascending order")
	ErrEntityTooSmall          = errors.New("multipart part is smaller than 5 MiB")
	ErrQueueNotFound           = errors.New("queue not found")
	ErrInvalidQueueAttribute   = errors.New("invalid queue attribute")
	ErrMissingMessageParameter = errors.New("missing message parameter")
	ErrInvalidMessageParameter = errors.New("invalid message parameter")
	ErrReceiptInvalid          = errors.New("receipt handle is invalid")
)

type Object struct {
	Key          string            `json:"key"`
	ETag         string            `json:"etag"`
	Size         int64             `json:"size"`
	LastModified time.Time         `json:"lastModified"`
	ContentType  string            `json:"contentType,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	File         string            `json:"file"`
}

type Notification struct {
	ID       string   `json:"id"`
	QueueARN string   `json:"queueArn"`
	Events   []string `json:"events"`
	Prefix   string   `json:"prefix,omitempty"`
	Suffix   string   `json:"suffix,omitempty"`
}

type Bucket struct {
	Name          string            `json:"name"`
	CreatedAt     time.Time         `json:"createdAt"`
	Objects       map[string]Object `json:"objects"`
	Notifications []Notification    `json:"notifications,omitempty"`
}

type MultipartPart struct {
	PartNumber   int       `json:"partNumber"`
	ETag         string    `json:"etag"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"lastModified"`
	File         string    `json:"file"`
}

type MultipartUpload struct {
	ID          string                `json:"id"`
	Bucket      string                `json:"bucket"`
	Key         string                `json:"key"`
	CreatedAt   time.Time             `json:"createdAt"`
	ContentType string                `json:"contentType,omitempty"`
	Metadata    map[string]string     `json:"metadata,omitempty"`
	Parts       map[int]MultipartPart `json:"parts"`
}

type CompletedMultipartPart struct {
	PartNumber int
	ETag       string
}

type Message struct {
	MessageID              string                      `json:"messageId"`
	Body                   string                      `json:"body"`
	MD5OfBody              string                      `json:"md5OfBody"`
	SentAt                 time.Time                   `json:"sentAt"`
	VisibleAt              time.Time                   `json:"visibleAt"`
	ReceiptHandle          string                      `json:"receiptHandle,omitempty"`
	ReceiveCount           int                         `json:"receiveCount"`
	MessageGroupID         string                      `json:"messageGroupId,omitempty"`
	MessageDeduplicationID string                      `json:"messageDeduplicationId,omitempty"`
	SequenceNumber         string                      `json:"sequenceNumber,omitempty"`
	MessageAttributes      map[string]MessageAttribute `json:"messageAttributes,omitempty"`
}

type MessageAttribute struct {
	DataType    string `json:"DataType"`
	StringValue string `json:"StringValue,omitempty"`
	BinaryValue []byte `json:"BinaryValue,omitempty"`
}

type Queue struct {
	Name               string                         `json:"name"`
	CreatedAt          time.Time                      `json:"createdAt"`
	Attributes         map[string]string              `json:"attributes"`
	Messages           []Message                      `json:"messages"`
	Deduplication      map[string]DeduplicationRecord `json:"deduplication,omitempty"`
	NextSequenceNumber uint64                         `json:"nextSequenceNumber,omitempty"`
}

type DeduplicationRecord struct {
	MessageID      string    `json:"messageId"`
	SequenceNumber string    `json:"sequenceNumber"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

type SendMessageOptions struct {
	MessageGroupID         string
	MessageDeduplicationID string
	DelaySpecified         bool
}

type redrivePolicy struct {
	DeadLetterTargetARN string
	MaxReceiveCount     int
}

type redrivePolicyJSON struct {
	DeadLetterTargetARN string          `json:"deadLetterTargetArn"`
	MaxReceiveCount     json.RawMessage `json:"maxReceiveCount"`
}

type snapshot struct {
	Buckets             map[string]*Bucket             `json:"buckets"`
	MultipartUploads    map[string]*MultipartUpload    `json:"multipartUploads,omitempty"`
	Queues              map[string]*Queue              `json:"queues"`
	DynamoTables        map[string]*DynamoTable        `json:"dynamoTables,omitempty"`
	GCSBuckets          map[string]*GCSBucket          `json:"gcsBuckets,omitempty"`
	PubSubTopics        map[string]*PubSubTopic        `json:"pubSubTopics,omitempty"`
	PubSubSubscriptions map[string]*PubSubSubscription `json:"pubSubSubscriptions,omitempty"`
	FirestoreDocuments  map[string]*FirestoreDocument  `json:"firestoreDocuments,omitempty"`
	Secrets             map[string]*Secret             `json:"secrets,omitempty"`
	KMSKeyRings         map[string]*KMSKeyRing         `json:"kmsKeyRings,omitempty"`
	KMSCryptoKeys       map[string]*KMSCryptoKey       `json:"kmsCryptoKeys,omitempty"`
	IAMServiceAccounts  map[string]*IAMServiceAccount  `json:"iamServiceAccounts,omitempty"`
	FCMMessages         []FCMMessage                   `json:"fcmMessages,omitempty"`
	VertexGenerations   []VertexGeneration             `json:"vertexGenerations,omitempty"`
}

type Store struct {
	mu      sync.Mutex
	dir     string
	objects string
	data    snapshot
	now     func() time.Time
}

func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("data directory is required")
	}
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:     dir,
		objects: filepath.Join(dir, "objects"),
		data: snapshot{
			Buckets: map[string]*Bucket{}, MultipartUploads: map[string]*MultipartUpload{}, Queues: map[string]*Queue{}, DynamoTables: map[string]*DynamoTable{}, GCSBuckets: map[string]*GCSBucket{},
			PubSubTopics: map[string]*PubSubTopic{}, PubSubSubscriptions: map[string]*PubSubSubscription{},
			FirestoreDocuments: map[string]*FirestoreDocument{}, Secrets: map[string]*Secret{},
			KMSKeyRings: map[string]*KMSKeyRing{}, KMSCryptoKeys: map[string]*KMSCryptoKey{},
			IAMServiceAccounts: map[string]*IAMServiceAccount{}, VertexGenerations: []VertexGeneration{},
		},
		now: time.Now,
	}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	if s.data.Buckets == nil {
		s.data.Buckets = map[string]*Bucket{}
	}
	if s.data.MultipartUploads == nil {
		s.data.MultipartUploads = map[string]*MultipartUpload{}
	}
	if s.data.Queues == nil {
		s.data.Queues = map[string]*Queue{}
	}
	if s.data.DynamoTables == nil {
		s.data.DynamoTables = map[string]*DynamoTable{}
	}
	for _, table := range s.data.DynamoTables {
		if table.Items == nil {
			table.Items = map[string]DynamoItem{}
		}
	}
	for _, queue := range s.data.Queues {
		if queue.Attributes == nil {
			queue.Attributes = map[string]string{}
		}
		if queue.Deduplication == nil {
			queue.Deduplication = map[string]DeduplicationRecord{}
		}
	}
	if s.data.GCSBuckets == nil {
		s.data.GCSBuckets = map[string]*GCSBucket{}
	}
	if s.data.PubSubTopics == nil {
		s.data.PubSubTopics = map[string]*PubSubTopic{}
	}
	if s.data.PubSubSubscriptions == nil {
		s.data.PubSubSubscriptions = map[string]*PubSubSubscription{}
	}
	if s.data.FirestoreDocuments == nil {
		s.data.FirestoreDocuments = map[string]*FirestoreDocument{}
	}
	if s.data.Secrets == nil {
		s.data.Secrets = map[string]*Secret{}
	}
	if s.data.KMSKeyRings == nil {
		s.data.KMSKeyRings = map[string]*KMSKeyRing{}
	}
	if s.data.KMSCryptoKeys == nil {
		s.data.KMSCryptoKeys = map[string]*KMSCryptoKey{}
	}
	if s.data.IAMServiceAccounts == nil {
		s.data.IAMServiceAccounts = map[string]*IAMServiceAccount{}
	}
	if s.data.FCMMessages == nil {
		s.data.FCMMessages = []FCMMessage{}
	}
	if s.data.VertexGenerations == nil {
		s.data.VertexGenerations = []VertexGeneration{}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".state-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(s.dir, "state.json"))
}

func (s *Store) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = snapshot{
		Buckets: map[string]*Bucket{}, MultipartUploads: map[string]*MultipartUpload{}, Queues: map[string]*Queue{}, DynamoTables: map[string]*DynamoTable{}, GCSBuckets: map[string]*GCSBucket{},
		PubSubTopics: map[string]*PubSubTopic{}, PubSubSubscriptions: map[string]*PubSubSubscription{},
		FirestoreDocuments: map[string]*FirestoreDocument{}, Secrets: map[string]*Secret{},
		KMSKeyRings: map[string]*KMSKeyRing{}, KMSCryptoKeys: map[string]*KMSCryptoKey{},
		IAMServiceAccounts: map[string]*IAMServiceAccount{},
		FCMMessages:        []FCMMessage{}, VertexGenerations: []VertexGeneration{},
	}
	entries, err := os.ReadDir(s.objects)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			_ = os.Remove(filepath.Join(s.objects, entry.Name()))
		}
	}
	return s.saveLocked()
}

// ResetWorkloadData clears data produced by local tests while preserving
// provisioned resources, secrets and key material. This keeps exported local
// credentials valid across repeated E2E runs.
func (s *Store) ResetWorkloadData() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	files := make([]string, 0)
	buckets := make(map[string]*Bucket, len(s.data.Buckets))
	for name, bucket := range s.data.Buckets {
		for _, object := range bucket.Objects {
			files = append(files, object.File)
		}
		cloned := *bucket
		cloned.Objects = map[string]Object{}
		cloned.Notifications = append([]Notification(nil), bucket.Notifications...)
		buckets[name] = &cloned
	}
	for _, upload := range s.data.MultipartUploads {
		for _, part := range upload.Parts {
			files = append(files, part.File)
		}
	}
	queues := make(map[string]*Queue, len(s.data.Queues))
	for name, queue := range s.data.Queues {
		cloned := *cloneQueue(queue)
		cloned.Messages = []Message{}
		cloned.Deduplication = map[string]DeduplicationRecord{}
		cloned.NextSequenceNumber = 0
		queues[name] = &cloned
	}
	dynamoTables := make(map[string]*DynamoTable, len(s.data.DynamoTables))
	for name, table := range s.data.DynamoTables {
		cloned := cloneDynamoTable(table)
		cloned.Items = map[string]DynamoItem{}
		dynamoTables[name] = &cloned
	}
	gcsBuckets := make(map[string]*GCSBucket, len(s.data.GCSBuckets))
	for name, bucket := range s.data.GCSBuckets {
		for _, object := range bucket.Objects {
			files = append(files, object.File)
		}
		cloned := cloneGCSBucket(bucket)
		cloned.Objects = map[string]GCSObject{}
		gcsBuckets[name] = &cloned
	}
	subscriptions := make(map[string]*PubSubSubscription, len(s.data.PubSubSubscriptions))
	for name, subscription := range s.data.PubSubSubscriptions {
		cloned := clonePubSubSubscription(subscription)
		cloned.Messages = []PubSubMessage{}
		subscriptions[name] = &cloned
	}

	previous := s.data
	s.data = snapshot{
		Buckets: buckets, MultipartUploads: map[string]*MultipartUpload{}, Queues: queues, DynamoTables: dynamoTables, GCSBuckets: gcsBuckets,
		PubSubTopics: s.data.PubSubTopics, PubSubSubscriptions: subscriptions,
		FirestoreDocuments: map[string]*FirestoreDocument{}, Secrets: s.data.Secrets,
		KMSKeyRings: s.data.KMSKeyRings, KMSCryptoKeys: s.data.KMSCryptoKeys,
		IAMServiceAccounts: s.data.IAMServiceAccounts,
		FCMMessages:        []FCMMessage{}, VertexGenerations: []VertexGeneration{},
	}
	if err := s.saveLocked(); err != nil {
		s.data = previous
		return err
	}
	for _, file := range files {
		_ = os.Remove(filepath.Join(s.objects, file))
	}
	return nil
}

func (s *Store) CreateBucket(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Buckets[name]; ok {
		return nil
	}
	s.data.Buckets[name] = &Bucket{Name: name, CreatedAt: s.now().UTC(), Objects: map[string]Object{}}
	return s.saveLocked()
}

func (s *Store) DeleteBucket(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Buckets[name]
	if !ok {
		return ErrBucketNotFound
	}
	if len(b.Objects) != 0 {
		return ErrBucketNotEmpty
	}
	for _, upload := range s.data.MultipartUploads {
		if upload.Bucket == name {
			return ErrBucketNotEmpty
		}
	}
	delete(s.data.Buckets, name)
	return s.saveLocked()
}

func (s *Store) HasBucket(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data.Buckets[name]
	return ok
}

func (s *Store) ListBuckets() []Bucket {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Bucket, 0, len(s.data.Buckets))
	for _, b := range s.data.Buckets {
		result = append(result, *b)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) PutObject(bucket, key string, body []byte, contentType string, metadata map[string]string) (Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Buckets[bucket]
	if !ok {
		return Object{}, ErrBucketNotFound
	}
	hash := sha256.Sum256([]byte(bucket + "\x00" + key))
	file := hex.EncodeToString(hash[:])
	tmp, err := os.CreateTemp(s.objects, ".object-*")
	if err != nil {
		return Object{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return Object{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Object{}, err
	}
	if err := tmp.Close(); err != nil {
		return Object{}, err
	}
	if err := os.Rename(tmpName, filepath.Join(s.objects, file)); err != nil {
		return Object{}, err
	}
	sum := md5.Sum(body)
	obj := Object{Key: key, ETag: hex.EncodeToString(sum[:]), Size: int64(len(body)), LastModified: s.now().UTC(), ContentType: contentType, Metadata: metadata, File: file}
	b.Objects[key] = obj
	if err := s.enqueueObjectEventLocked(b, obj, "ObjectCreated:Put"); err != nil {
		return Object{}, err
	}
	if err := s.saveLocked(); err != nil {
		return Object{}, err
	}
	return obj, nil
}

func (s *Store) enqueueObjectEventLocked(bucket *Bucket, obj Object, eventName string) error {
	for _, n := range bucket.Notifications {
		if !matchesObjectCreated(n.Events, eventName) || !strings.HasPrefix(obj.Key, n.Prefix) || !strings.HasSuffix(obj.Key, n.Suffix) {
			continue
		}
		parts := strings.Split(n.QueueARN, ":")
		if len(parts) == 0 {
			continue
		}
		q, ok := s.data.Queues[parts[len(parts)-1]]
		if !ok {
			continue
		}
		event := map[string]any{"Records": []any{map[string]any{
			"eventVersion": "2.1", "eventSource": "aws:s3", "awsRegion": "us-east-1", "eventTime": s.now().UTC().Format(time.RFC3339Nano), "eventName": eventName,
			"userIdentity":      map[string]string{"principalId": defaultAccountID},
			"requestParameters": map[string]string{"sourceIPAddress": "127.0.0.1"},
			"responseElements":  map[string]string{"x-amz-request-id": newID()},
			"s3":                map[string]any{"s3SchemaVersion": "1.0", "configurationId": n.ID, "bucket": map[string]any{"name": bucket.Name, "arn": "arn:aws:s3:::" + bucket.Name}, "object": map[string]any{"key": obj.Key, "size": obj.Size, "eTag": obj.ETag}},
		}}}
		raw, _ := json.Marshal(event)
		s.enqueueLocked(q, string(raw), nil, 0)
	}
	return nil
}

func matchesObjectCreated(events []string, eventName string) bool {
	for _, event := range events {
		if event == "s3:ObjectCreated:*" || event == "s3:"+eventName {
			return true
		}
	}
	return false
}

func (s *Store) GetObject(bucket, key string) (Object, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Buckets[bucket]
	if !ok {
		return Object{}, nil, ErrBucketNotFound
	}
	obj, ok := b.Objects[key]
	if !ok {
		return Object{}, nil, ErrObjectNotFound
	}
	body, err := os.ReadFile(filepath.Join(s.objects, obj.File))
	return obj, body, err
}

func (s *Store) DeleteObject(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Buckets[bucket]
	if !ok {
		return ErrBucketNotFound
	}
	obj, ok := b.Objects[key]
	if !ok {
		return nil
	}
	delete(b.Objects, key)
	if err := s.saveLocked(); err != nil {
		b.Objects[key] = obj
		return err
	}
	_ = os.Remove(filepath.Join(s.objects, obj.File))
	return nil
}

func (s *Store) ListObjects(bucket, prefix, after string, limit int) ([]Object, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Buckets[bucket]
	if !ok {
		return nil, false, ErrBucketNotFound
	}
	keys := make([]string, 0, len(b.Objects))
	for key := range b.Objects {
		if strings.HasPrefix(key, prefix) && key > after {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	truncated := len(keys) > limit
	if truncated {
		keys = keys[:limit]
	}
	result := make([]Object, 0, len(keys))
	for _, key := range keys {
		result = append(result, b.Objects[key])
	}
	return result, truncated, nil
}

func (s *Store) CreateMultipartUpload(bucket, key, contentType string, metadata map[string]string) (MultipartUpload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Buckets[bucket]; !ok {
		return MultipartUpload{}, ErrBucketNotFound
	}
	upload := &MultipartUpload{
		ID:          newID(),
		Bucket:      bucket,
		Key:         key,
		CreatedAt:   s.now().UTC(),
		ContentType: contentType,
		Metadata:    cloneStringMap(metadata),
		Parts:       map[int]MultipartPart{},
	}
	s.data.MultipartUploads[upload.ID] = upload
	if err := s.saveLocked(); err != nil {
		delete(s.data.MultipartUploads, upload.ID)
		return MultipartUpload{}, err
	}
	return *cloneMultipartUpload(upload), nil
}

func (s *Store) UploadMultipartPart(bucket, key, uploadID string, partNumber int, body []byte) (MultipartPart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if partNumber < 1 || partNumber > 10_000 {
		return MultipartPart{}, ErrInvalidPart
	}
	upload, ok := s.data.MultipartUploads[uploadID]
	if !ok || upload.Bucket != bucket || upload.Key != key {
		return MultipartPart{}, ErrMultipartUploadNotFound
	}
	hash := sha256.Sum256([]byte("multipart\x00" + uploadID + "\x00" + strconv.Itoa(partNumber)))
	file := hex.EncodeToString(hash[:])
	tmp, err := os.CreateTemp(s.objects, ".multipart-*")
	if err != nil {
		return MultipartPart{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return MultipartPart{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return MultipartPart{}, err
	}
	if err := tmp.Close(); err != nil {
		return MultipartPart{}, err
	}
	if err := os.Rename(tmpName, filepath.Join(s.objects, file)); err != nil {
		return MultipartPart{}, err
	}
	sum := md5.Sum(body)
	part := MultipartPart{
		PartNumber: partNumber, ETag: hex.EncodeToString(sum[:]), Size: int64(len(body)),
		LastModified: s.now().UTC(), File: file,
	}
	previous, replaced := upload.Parts[partNumber]
	upload.Parts[partNumber] = part
	if err := s.saveLocked(); err != nil {
		if replaced {
			upload.Parts[partNumber] = previous
		} else {
			delete(upload.Parts, partNumber)
		}
		return MultipartPart{}, err
	}
	return part, nil
}

func (s *Store) ListMultipartParts(bucket, key, uploadID string) (MultipartUpload, []MultipartPart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	upload, ok := s.data.MultipartUploads[uploadID]
	if !ok || upload.Bucket != bucket || upload.Key != key {
		return MultipartUpload{}, nil, ErrMultipartUploadNotFound
	}
	parts := make([]MultipartPart, 0, len(upload.Parts))
	for _, part := range upload.Parts {
		parts = append(parts, part)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	return *cloneMultipartUpload(upload), parts, nil
}

func (s *Store) ListMultipartUploads(bucket, prefix string) ([]MultipartUpload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Buckets[bucket]; !ok {
		return nil, ErrBucketNotFound
	}
	uploads := make([]MultipartUpload, 0)
	for _, upload := range s.data.MultipartUploads {
		if upload.Bucket == bucket && strings.HasPrefix(upload.Key, prefix) {
			uploads = append(uploads, *cloneMultipartUpload(upload))
		}
	}
	sort.Slice(uploads, func(i, j int) bool {
		if uploads[i].Key == uploads[j].Key {
			return uploads[i].ID < uploads[j].ID
		}
		return uploads[i].Key < uploads[j].Key
	})
	return uploads, nil
}

func (s *Store) CompleteMultipartUpload(bucket, key, uploadID string, completed []CompletedMultipartPart) (Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	upload, ok := s.data.MultipartUploads[uploadID]
	if !ok || upload.Bucket != bucket || upload.Key != key {
		return Object{}, ErrMultipartUploadNotFound
	}
	if len(completed) == 0 {
		return Object{}, ErrInvalidPart
	}
	parts := make([]MultipartPart, 0, len(completed))
	previousNumber := 0
	for i, requested := range completed {
		if requested.PartNumber <= previousNumber {
			return Object{}, ErrInvalidPartOrder
		}
		part, exists := upload.Parts[requested.PartNumber]
		if !exists || !strings.EqualFold(strings.Trim(requested.ETag, "\""), part.ETag) {
			return Object{}, ErrInvalidPart
		}
		if i < len(completed)-1 && part.Size < 5*1024*1024 {
			return Object{}, ErrEntityTooSmall
		}
		parts = append(parts, part)
		previousNumber = requested.PartNumber
	}

	tmp, err := os.CreateTemp(s.objects, ".multipart-complete-*")
	if err != nil {
		return Object{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	combinedMD5 := md5.New()
	var totalSize int64
	for _, part := range parts {
		partFile, err := os.Open(filepath.Join(s.objects, part.File))
		if err != nil {
			tmp.Close()
			return Object{}, err
		}
		written, copyErr := io.Copy(tmp, partFile)
		closeErr := partFile.Close()
		if copyErr != nil {
			tmp.Close()
			return Object{}, copyErr
		}
		if closeErr != nil {
			tmp.Close()
			return Object{}, closeErr
		}
		if written != part.Size {
			tmp.Close()
			return Object{}, fmt.Errorf("multipart part %d size changed", part.PartNumber)
		}
		digest, err := hex.DecodeString(part.ETag)
		if err != nil {
			tmp.Close()
			return Object{}, err
		}
		_, _ = combinedMD5.Write(digest)
		totalSize += written
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Object{}, err
	}
	if err := tmp.Close(); err != nil {
		return Object{}, err
	}
	objectHash := sha256.Sum256([]byte(bucket + "\x00" + key))
	objectFile := hex.EncodeToString(objectHash[:])
	if err := os.Rename(tmpName, filepath.Join(s.objects, objectFile)); err != nil {
		return Object{}, err
	}

	b := s.data.Buckets[bucket]
	obj := Object{
		Key: key, ETag: fmt.Sprintf("%x-%d", combinedMD5.Sum(nil), len(parts)), Size: totalSize,
		LastModified: s.now().UTC(), ContentType: upload.ContentType, Metadata: cloneStringMap(upload.Metadata), File: objectFile,
	}
	previousObject, replaced := b.Objects[key]
	b.Objects[key] = obj
	delete(s.data.MultipartUploads, uploadID)
	if err := s.enqueueObjectEventLocked(b, obj, "ObjectCreated:CompleteMultipartUpload"); err != nil {
		return Object{}, err
	}
	if err := s.saveLocked(); err != nil {
		s.data.MultipartUploads[uploadID] = upload
		if replaced {
			b.Objects[key] = previousObject
		} else {
			delete(b.Objects, key)
		}
		return Object{}, err
	}
	for _, part := range upload.Parts {
		_ = os.Remove(filepath.Join(s.objects, part.File))
	}
	return obj, nil
}

func (s *Store) AbortMultipartUpload(bucket, key, uploadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	upload, ok := s.data.MultipartUploads[uploadID]
	if !ok || upload.Bucket != bucket || upload.Key != key {
		return ErrMultipartUploadNotFound
	}
	delete(s.data.MultipartUploads, uploadID)
	if err := s.saveLocked(); err != nil {
		s.data.MultipartUploads[uploadID] = upload
		return err
	}
	for _, part := range upload.Parts {
		_ = os.Remove(filepath.Join(s.objects, part.File))
	}
	return nil
}

func cloneMultipartUpload(upload *MultipartUpload) *MultipartUpload {
	if upload == nil {
		return nil
	}
	cloned := *upload
	cloned.Metadata = cloneStringMap(upload.Metadata)
	cloned.Parts = make(map[int]MultipartPart, len(upload.Parts))
	for number, part := range upload.Parts {
		cloned.Parts[number] = part
	}
	return &cloned
}

func (s *Store) SetNotifications(bucket string, notifications []Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Buckets[bucket]
	if !ok {
		return ErrBucketNotFound
	}
	for _, n := range notifications {
		parts := strings.Split(n.QueueARN, ":")
		if len(parts) == 0 {
			return ErrQueueNotFound
		}
		if _, ok := s.data.Queues[parts[len(parts)-1]]; !ok {
			return ErrQueueNotFound
		}
	}
	b.Notifications = notifications
	return s.saveLocked()
}

func (s *Store) Notifications(bucket string) ([]Notification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Buckets[bucket]
	if !ok {
		return nil, ErrBucketNotFound
	}
	return append([]Notification(nil), b.Notifications...), nil
}

func (s *Store) CreateQueue(name string, attrs map[string]string) (*Queue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q, ok := s.data.Queues[name]; ok {
		return cloneQueue(q), nil
	}
	if err := validateQueueCreation(name, attrs); err != nil {
		return nil, err
	}
	if err := s.validateQueueAttributesLocked(name, attrs); err != nil {
		return nil, err
	}
	defaults := map[string]string{"VisibilityTimeout": "30", "DelaySeconds": "0", "ReceiveMessageWaitTimeSeconds": "0", "MessageRetentionPeriod": "345600"}
	if strings.HasSuffix(name, ".fifo") {
		defaults["FifoQueue"] = "true"
		defaults["ContentBasedDeduplication"] = "false"
	}
	for k, v := range attrs {
		if k == "RedrivePolicy" && strings.TrimSpace(v) == "" {
			continue
		}
		defaults[k] = v
	}
	q := &Queue{Name: name, CreatedAt: s.now().UTC(), Attributes: defaults, Messages: []Message{}, Deduplication: map[string]DeduplicationRecord{}}
	s.data.Queues[name] = q
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return cloneQueue(q), nil
}

func cloneQueue(q *Queue) *Queue {
	c := *q
	c.Attributes = map[string]string{}
	for k, v := range q.Attributes {
		c.Attributes[k] = v
	}
	c.Messages = append([]Message(nil), q.Messages...)
	c.Deduplication = make(map[string]DeduplicationRecord, len(q.Deduplication))
	for key, record := range q.Deduplication {
		c.Deduplication[key] = record
	}
	return &c
}

func (s *Store) Queue(name string) (*Queue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.data.Queues[name]
	if !ok {
		return nil, ErrQueueNotFound
	}
	return cloneQueue(q), nil
}

func (s *Store) ListQueues(prefix string) []Queue {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []Queue{}
	for _, q := range s.data.Queues {
		if strings.HasPrefix(q.Name, prefix) {
			result = append(result, *cloneQueue(q))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) DeleteQueue(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Queues[name]; !ok {
		return ErrQueueNotFound
	}
	delete(s.data.Queues, name)
	return s.saveLocked()
}

func (s *Store) PurgeQueue(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.data.Queues[name]
	if !ok {
		return ErrQueueNotFound
	}
	q.Messages = []Message{}
	return s.saveLocked()
}

func (s *Store) SendMessage(name, body string, attrs map[string]MessageAttribute, delay int) (Message, error) {
	return s.SendMessageWithOptions(name, body, attrs, delay, SendMessageOptions{})
}

func (s *Store) SendMessageWithOptions(name, body string, attrs map[string]MessageAttribute, delay int, options SendMessageOptions) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.data.Queues[name]
	if !ok {
		return Message{}, ErrQueueNotFound
	}
	if delay < 0 {
		delay = intValue(q.Attributes["DelaySeconds"], 0)
	}
	if delay < 0 || delay > 900 {
		return Message{}, fmt.Errorf("%w: DelaySeconds must be between 0 and 900", ErrInvalidMessageParameter)
	}
	fifo := queueIsFIFO(q)
	if fifo && options.DelaySpecified && delay != 0 {
		return Message{}, fmt.Errorf("%w: DelaySeconds is not supported for individual FIFO messages", ErrInvalidMessageParameter)
	}
	if !fifo && options.MessageDeduplicationID != "" {
		return Message{}, fmt.Errorf("%w: MessageDeduplicationId applies only to FIFO queues", ErrInvalidMessageParameter)
	}
	if fifo {
		if q.Deduplication == nil {
			q.Deduplication = map[string]DeduplicationRecord{}
		}
		if options.MessageGroupID == "" {
			return Message{}, fmt.Errorf("%w: MessageGroupId is required for FIFO queues", ErrMissingMessageParameter)
		}
		if !validFIFOIdentifier(options.MessageGroupID) {
			return Message{}, fmt.Errorf("%w: MessageGroupId must contain 1 to 128 supported characters", ErrInvalidMessageParameter)
		}
		if options.MessageDeduplicationID == "" {
			if !strings.EqualFold(q.Attributes["ContentBasedDeduplication"], "true") {
				return Message{}, fmt.Errorf("%w: MessageDeduplicationId is required when content-based deduplication is disabled", ErrMissingMessageParameter)
			}
			digest := sha256.Sum256([]byte(body))
			options.MessageDeduplicationID = hex.EncodeToString(digest[:])
		}
		if !validFIFOIdentifier(options.MessageDeduplicationID) {
			return Message{}, fmt.Errorf("%w: MessageDeduplicationId must contain 1 to 128 supported characters", ErrInvalidMessageParameter)
		}
		now := s.now().UTC()
		for key, record := range q.Deduplication {
			if !record.ExpiresAt.After(now) {
				delete(q.Deduplication, key)
			}
		}
		dedupKey := fifoDeduplicationKey(q, options.MessageGroupID, options.MessageDeduplicationID)
		if record, duplicate := q.Deduplication[dedupKey]; duplicate {
			sum := md5.Sum([]byte(body))
			return Message{
				MessageID: record.MessageID, MD5OfBody: hex.EncodeToString(sum[:]),
				MessageGroupID: options.MessageGroupID, MessageDeduplicationID: options.MessageDeduplicationID,
				SequenceNumber: record.SequenceNumber,
			}, nil
		}
		sequenceNumber := nextFIFOSequenceNumber(q)
		m := s.enqueueMessageLocked(q, body, attrs, delay, options.MessageGroupID, options.MessageDeduplicationID, sequenceNumber)
		q.Deduplication[dedupKey] = DeduplicationRecord{MessageID: m.MessageID, SequenceNumber: sequenceNumber, ExpiresAt: now.Add(5 * time.Minute)}
		if err := s.saveLocked(); err != nil {
			return Message{}, err
		}
		return m, nil
	}
	m := s.enqueueMessageLocked(q, body, attrs, delay, options.MessageGroupID, "", "")
	if err := s.saveLocked(); err != nil {
		return Message{}, err
	}
	return m, nil
}

func (s *Store) enqueueLocked(q *Queue, body string, attrs map[string]MessageAttribute, delay int) Message {
	return s.enqueueMessageLocked(q, body, attrs, delay, "", "", "")
}

func (s *Store) enqueueMessageLocked(q *Queue, body string, attrs map[string]MessageAttribute, delay int, groupID, deduplicationID, sequenceNumber string) Message {
	if delay < 0 {
		delay = 0
	}
	sum := md5.Sum([]byte(body))
	m := Message{
		MessageID: newID(), Body: body, MD5OfBody: hex.EncodeToString(sum[:]),
		SentAt: s.now().UTC(), VisibleAt: s.now().UTC().Add(time.Duration(delay) * time.Second),
		MessageGroupID: groupID, MessageDeduplicationID: deduplicationID, SequenceNumber: sequenceNumber,
		MessageAttributes: attrs,
	}
	q.Messages = append(q.Messages, m)
	return m
}

func (s *Store) ReceiveMessages(name string, max, visibility int) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.data.Queues[name]
	if !ok {
		return nil, ErrQueueNotFound
	}
	if max < 1 {
		max = 1
	}
	if max > 10 {
		max = 10
	}
	if visibility < 0 {
		visibility = intValue(q.Attributes["VisibilityTimeout"], 30)
	}
	now := s.now().UTC()
	result := []Message{}
	policy, hasRedrive := redrivePolicy{}, false
	if raw := q.Attributes["RedrivePolicy"]; raw != "" {
		if parsed, err := parseRedrivePolicy(raw); err == nil {
			policy = parsed
			hasRedrive = true
		}
	}
	var deadLetterQueue *Queue
	if hasRedrive {
		deadLetterQueue = s.data.Queues[queueNameFromARN(policy.DeadLetterTargetARN)]
	}
	fifo := queueIsFIFO(q)
	blockedGroups := map[string]bool{}
	changed := false
	remaining := make([]Message, 0, len(q.Messages))
	for _, message := range q.Messages {
		if fifo && blockedGroups[message.MessageGroupID] {
			remaining = append(remaining, message)
			continue
		}
		if message.VisibleAt.After(now) {
			remaining = append(remaining, message)
			if fifo {
				blockedGroups[message.MessageGroupID] = true
			}
			continue
		}
		if deadLetterQueue != nil && message.ReceiveCount >= policy.MaxReceiveCount {
			message.ReceiptHandle = ""
			message.VisibleAt = now
			if queueIsFIFO(deadLetterQueue) {
				if deadLetterQueue.Deduplication == nil {
					deadLetterQueue.Deduplication = map[string]DeduplicationRecord{}
				}
				message.MessageDeduplicationID = message.MessageID
				message.SequenceNumber = nextFIFOSequenceNumber(deadLetterQueue)
				message.SentAt = now
				key := fifoDeduplicationKey(deadLetterQueue, message.MessageGroupID, message.MessageDeduplicationID)
				deadLetterQueue.Deduplication[key] = DeduplicationRecord{
					MessageID: message.MessageID, SequenceNumber: message.SequenceNumber, ExpiresAt: now.Add(5 * time.Minute),
				}
			}
			deadLetterQueue.Messages = append(deadLetterQueue.Messages, message)
			changed = true
			continue
		}
		if len(result) < max {
			message.ReceiveCount++
			message.ReceiptHandle = newID()
			message.VisibleAt = now.Add(time.Duration(visibility) * time.Second)
			result = append(result, message)
			changed = true
		} else if fifo {
			blockedGroups[message.MessageGroupID] = true
		}
		remaining = append(remaining, message)
	}
	q.Messages = remaining
	if changed {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) DeleteMessage(name, receipt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.data.Queues[name]
	if !ok {
		return ErrQueueNotFound
	}
	for i := range q.Messages {
		if q.Messages[i].ReceiptHandle == receipt {
			q.Messages = append(q.Messages[:i], q.Messages[i+1:]...)
			return s.saveLocked()
		}
	}
	return ErrReceiptInvalid
}

func (s *Store) ChangeVisibility(name, receipt string, seconds int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.data.Queues[name]
	if !ok {
		return ErrQueueNotFound
	}
	for i := range q.Messages {
		if q.Messages[i].ReceiptHandle == receipt {
			q.Messages[i].VisibleAt = s.now().UTC().Add(time.Duration(seconds) * time.Second)
			return s.saveLocked()
		}
	}
	return ErrReceiptInvalid
}

func (s *Store) SetQueueAttributes(name string, attrs map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.data.Queues[name]
	if !ok {
		return ErrQueueNotFound
	}
	if err := validateQueueAttributeMutation(q, attrs); err != nil {
		return err
	}
	if err := s.validateQueueAttributesLocked(name, attrs); err != nil {
		return err
	}
	for k, v := range attrs {
		if k == "RedrivePolicy" && strings.TrimSpace(v) == "" {
			delete(q.Attributes, k)
			continue
		}
		q.Attributes[k] = v
	}
	return s.saveLocked()
}

func validateQueueCreation(name string, attrs map[string]string) error {
	fifo, err := queueAttributeBool(attrs, "FifoQueue", false)
	if err != nil {
		return err
	}
	if fifo != strings.HasSuffix(name, ".fifo") {
		return fmt.Errorf("%w: FIFO queue names must end with .fifo and set FifoQueue to true", ErrInvalidQueueAttribute)
	}
	return validateFIFOQueueAttributes(fifo, attrs, nil)
}

func validateQueueAttributeMutation(queue *Queue, attrs map[string]string) error {
	if _, exists := attrs["FifoQueue"]; exists {
		return fmt.Errorf("%w: FifoQueue cannot be changed after queue creation", ErrInvalidQueueAttribute)
	}
	return validateFIFOQueueAttributes(queueIsFIFO(queue), attrs, queue.Attributes)
}

func validateFIFOQueueAttributes(fifo bool, attrs, current map[string]string) error {
	if _, exists := attrs["ContentBasedDeduplication"]; exists {
		if !fifo {
			return fmt.Errorf("%w: ContentBasedDeduplication applies only to FIFO queues", ErrInvalidQueueAttribute)
		}
		if _, err := queueAttributeBool(attrs, "ContentBasedDeduplication", false); err != nil {
			return err
		}
	}
	scope := "queue"
	if current != nil && current["DeduplicationScope"] != "" {
		scope = current["DeduplicationScope"]
	}
	if value, exists := attrs["DeduplicationScope"]; exists {
		if !fifo || (value != "queue" && value != "messageGroup") {
			return fmt.Errorf("%w: DeduplicationScope must be queue or messageGroup on a FIFO queue", ErrInvalidQueueAttribute)
		}
		scope = value
	}
	throughput := "perQueue"
	if current != nil && current["FifoThroughputLimit"] != "" {
		throughput = current["FifoThroughputLimit"]
	}
	if value, exists := attrs["FifoThroughputLimit"]; exists {
		if !fifo || (value != "perQueue" && value != "perMessageGroupId") {
			return fmt.Errorf("%w: FifoThroughputLimit must be perQueue or perMessageGroupId on a FIFO queue", ErrInvalidQueueAttribute)
		}
		throughput = value
	}
	if throughput == "perMessageGroupId" && scope != "messageGroup" {
		return fmt.Errorf("%w: perMessageGroupId throughput requires messageGroup deduplication scope", ErrInvalidQueueAttribute)
	}
	return nil
}

func queueAttributeBool(attrs map[string]string, name string, fallback bool) (bool, error) {
	value, exists := attrs[name]
	if !exists {
		return fallback, nil
	}
	switch strings.ToLower(value) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%w: %s must be true or false", ErrInvalidQueueAttribute, name)
	}
}

func queueIsFIFO(queue *Queue) bool {
	return queue != nil && strings.EqualFold(queue.Attributes["FifoQueue"], "true") && strings.HasSuffix(queue.Name, ".fifo")
}

func validFIFOIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func fifoDeduplicationKey(queue *Queue, groupID, deduplicationID string) string {
	if queue.Attributes["DeduplicationScope"] == "messageGroup" {
		return groupID + "\x00" + deduplicationID
	}
	return deduplicationID
}

func nextFIFOSequenceNumber(queue *Queue) string {
	queue.NextSequenceNumber++
	return strconv.FormatUint(queue.NextSequenceNumber, 10)
}

func (s *Store) validateQueueAttributesLocked(sourceName string, attrs map[string]string) error {
	raw, exists := attrs["RedrivePolicy"]
	if !exists || strings.TrimSpace(raw) == "" {
		return nil
	}
	policy, err := parseRedrivePolicy(raw)
	if err != nil {
		return fmt.Errorf("%w: RedrivePolicy: %v", ErrInvalidQueueAttribute, err)
	}
	targetName := queueNameFromARN(policy.DeadLetterTargetARN)
	if targetName == "" {
		return fmt.Errorf("%w: RedrivePolicy deadLetterTargetArn is invalid", ErrInvalidQueueAttribute)
	}
	if _, ok := s.data.Queues[targetName]; !ok {
		return fmt.Errorf("%w: dead-letter queue does not exist", ErrInvalidQueueAttribute)
	}
	if targetName == sourceName {
		return fmt.Errorf("%w: a queue cannot target itself", ErrInvalidQueueAttribute)
	}
	if strings.HasSuffix(targetName, ".fifo") != strings.HasSuffix(sourceName, ".fifo") {
		return fmt.Errorf("%w: source and dead-letter queues must have the same type", ErrInvalidQueueAttribute)
	}
	return nil
}

func parseRedrivePolicy(raw string) (redrivePolicy, error) {
	var encoded redrivePolicyJSON
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		return redrivePolicy{}, err
	}
	if encoded.DeadLetterTargetARN == "" {
		return redrivePolicy{}, errors.New("deadLetterTargetArn is required")
	}
	var count int
	if err := json.Unmarshal(encoded.MaxReceiveCount, &count); err != nil {
		var value string
		if stringErr := json.Unmarshal(encoded.MaxReceiveCount, &value); stringErr != nil {
			return redrivePolicy{}, errors.New("maxReceiveCount must be an integer")
		}
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			return redrivePolicy{}, errors.New("maxReceiveCount must be an integer")
		}
		count = parsed
	}
	if count < 1 || count > 1000 {
		return redrivePolicy{}, errors.New("maxReceiveCount must be between 1 and 1000")
	}
	return redrivePolicy{DeadLetterTargetARN: encoded.DeadLetterTargetARN, MaxReceiveCount: count}, nil
}

func queueNameFromARN(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "sqs" || parts[3] != "us-east-1" || parts[4] != defaultAccountID || parts[5] == "" {
		return ""
	}
	return parts[5]
}

func (s *Store) QueueAttributes(name string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.data.Queues[name]
	if !ok {
		return nil, ErrQueueNotFound
	}
	now := s.now().UTC()
	visible := 0
	delayed := 0
	invisible := 0
	for _, m := range q.Messages {
		if m.ReceiveCount == 0 && m.VisibleAt.After(now) {
			delayed++
		} else if m.VisibleAt.After(now) {
			invisible++
		} else {
			visible++
		}
	}
	result := map[string]string{}
	for k, v := range q.Attributes {
		result[k] = v
	}
	result["QueueArn"] = "arn:aws:sqs:us-east-1:" + defaultAccountID + ":" + name
	result["CreatedTimestamp"] = fmt.Sprintf("%d", q.CreatedAt.Unix())
	result["LastModifiedTimestamp"] = result["CreatedTimestamp"]
	result["ApproximateNumberOfMessages"] = fmt.Sprintf("%d", visible)
	result["ApproximateNumberOfMessagesDelayed"] = fmt.Sprintf("%d", delayed)
	result["ApproximateNumberOfMessagesNotVisible"] = fmt.Sprintf("%d", invisible)
	return result, nil
}

func intValue(value string, fallback int) int {
	var n int
	if _, err := fmt.Sscanf(value, "%d", &n); err != nil {
		return fallback
	}
	return n
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func AccountID() string { return defaultAccountID }
