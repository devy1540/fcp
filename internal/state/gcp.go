package state

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrGCSBucketNotFound          = errors.New("GCS bucket not found")
	ErrGCSBucketNotEmpty          = errors.New("GCS bucket not empty")
	ErrGCSObjectNotFound          = errors.New("GCS object not found")
	ErrPubSubTopicNotFound        = errors.New("Pub/Sub topic not found")
	ErrPubSubSubscriptionNotFound = errors.New("Pub/Sub subscription not found")
)

type GCSObject struct {
	Name           string            `json:"name"`
	Bucket         string            `json:"bucket"`
	Size           int64             `json:"size"`
	ContentType    string            `json:"contentType,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	ETag           string            `json:"etag"`
	MD5Hash        string            `json:"md5Hash"`
	CRC32C         string            `json:"crc32c"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
	Generation     int64             `json:"generation"`
	Metageneration int64             `json:"metageneration"`
	File           string            `json:"file"`
}

type GCSBucket struct {
	Name         string               `json:"name"`
	Project      string               `json:"project"`
	Location     string               `json:"location"`
	StorageClass string               `json:"storageClass"`
	CreatedAt    time.Time            `json:"createdAt"`
	Objects      map[string]GCSObject `json:"objects"`
}

type PubSubTopic struct {
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
}

type PubSubMessage struct {
	MessageID       string            `json:"messageId"`
	Data            []byte            `json:"data"`
	Attributes      map[string]string `json:"attributes,omitempty"`
	OrderingKey     string            `json:"orderingKey,omitempty"`
	PublishTime     time.Time         `json:"publishTime"`
	AckID           string            `json:"ackId,omitempty"`
	VisibleAt       time.Time         `json:"visibleAt"`
	DeliveryAttempt int32             `json:"deliveryAttempt"`
}

type PubSubSubscription struct {
	Name                string            `json:"name"`
	Topic               string            `json:"topic"`
	AckDeadlineSeconds  int32             `json:"ackDeadlineSeconds"`
	DeadLetterTopic     string            `json:"deadLetterTopic,omitempty"`
	MaxDeliveryAttempts int32             `json:"maxDeliveryAttempts,omitempty"`
	Labels              map[string]string `json:"labels,omitempty"`
	EnableOrdering      bool              `json:"enableOrdering,omitempty"`
	CreatedAt           time.Time         `json:"createdAt"`
	Messages            []PubSubMessage   `json:"messages"`
}

func (s *Store) UpdatePubSubSubscription(name string, deadline int32, labels map[string]string, deadLetterTopic string, maxDeliveryAttempts int32, updateDeadline, updateLabels, updateDeadLetter bool) (PubSubSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data.PubSubSubscriptions[name]
	if !ok {
		return PubSubSubscription{}, ErrPubSubSubscriptionNotFound
	}
	if updateDeadLetter && deadLetterTopic != "" {
		if _, ok := s.data.PubSubTopics[deadLetterTopic]; !ok {
			return PubSubSubscription{}, ErrPubSubTopicNotFound
		}
	}
	if updateDeadline {
		sub.AckDeadlineSeconds = deadline
	}
	if updateLabels {
		sub.Labels = cloneStringMap(labels)
	}
	if updateDeadLetter {
		sub.DeadLetterTopic = deadLetterTopic
		sub.MaxDeliveryAttempts = maxDeliveryAttempts
	}
	if err := s.saveLocked(); err != nil {
		return PubSubSubscription{}, err
	}
	return clonePubSubSubscription(sub), nil
}

func (s *Store) CreateGCSBucket(project, name, location, storageClass string) (GCSBucket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.data.GCSBuckets[name]; ok {
		return cloneGCSBucket(existing), nil
	}
	if location == "" {
		location = "US"
	}
	if storageClass == "" {
		storageClass = "STANDARD"
	}
	b := &GCSBucket{Name: name, Project: project, Location: strings.ToUpper(location), StorageClass: storageClass, CreatedAt: s.now().UTC(), Objects: map[string]GCSObject{}}
	s.data.GCSBuckets[name] = b
	if err := s.saveLocked(); err != nil {
		return GCSBucket{}, err
	}
	return cloneGCSBucket(b), nil
}

func cloneGCSBucket(b *GCSBucket) GCSBucket {
	c := *b
	c.Objects = make(map[string]GCSObject, len(b.Objects))
	for k, v := range b.Objects {
		c.Objects[k] = v
	}
	return c
}

func (s *Store) GCSBucket(name string) (GCSBucket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.GCSBuckets[name]
	if !ok {
		return GCSBucket{}, ErrGCSBucketNotFound
	}
	return cloneGCSBucket(b), nil
}

func (s *Store) ListGCSBuckets(project string) []GCSBucket {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []GCSBucket{}
	for _, b := range s.data.GCSBuckets {
		if project == "" || b.Project == project {
			result = append(result, cloneGCSBucket(b))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) DeleteGCSBucket(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.GCSBuckets[name]
	if !ok {
		return ErrGCSBucketNotFound
	}
	if len(b.Objects) > 0 {
		return ErrGCSBucketNotEmpty
	}
	delete(s.data.GCSBuckets, name)
	return s.saveLocked()
}

func (s *Store) PutGCSObject(bucket, name string, body []byte, contentType string, metadata map[string]string) (GCSObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.GCSBuckets[bucket]
	if !ok {
		return GCSObject{}, ErrGCSBucketNotFound
	}
	hash := sha256.Sum256([]byte("gcs\x00" + bucket + "\x00" + name))
	file := hex.EncodeToString(hash[:])
	tmp, err := os.CreateTemp(s.objects, ".gcs-object-*")
	if err != nil {
		return GCSObject{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return GCSObject{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return GCSObject{}, err
	}
	if err := tmp.Close(); err != nil {
		return GCSObject{}, err
	}
	if err := os.Rename(tmpName, filepath.Join(s.objects, file)); err != nil {
		return GCSObject{}, err
	}
	now := s.now().UTC()
	generation := now.UnixNano()
	created := now
	metageneration := int64(1)
	if previous, exists := b.Objects[name]; exists {
		created = previous.CreatedAt
		if generation <= previous.Generation {
			generation = previous.Generation + 1
		}
	}
	md5sum := md5.Sum(body)
	crc := crc32.Checksum(body, crc32.MakeTable(crc32.Castagnoli))
	crcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBytes, crc)
	obj := GCSObject{Name: name, Bucket: bucket, Size: int64(len(body)), ContentType: contentType, Metadata: cloneStringMap(metadata), ETag: base64.StdEncoding.EncodeToString(md5sum[:]), MD5Hash: base64.StdEncoding.EncodeToString(md5sum[:]), CRC32C: base64.StdEncoding.EncodeToString(crcBytes), CreatedAt: created, UpdatedAt: now, Generation: generation, Metageneration: metageneration, File: file}
	b.Objects[name] = obj
	if err := s.saveLocked(); err != nil {
		return GCSObject{}, err
	}
	return obj, nil
}

func (s *Store) GCSObject(bucket, name string) (GCSObject, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.GCSBuckets[bucket]
	if !ok {
		return GCSObject{}, nil, ErrGCSBucketNotFound
	}
	obj, ok := b.Objects[name]
	if !ok {
		return GCSObject{}, nil, ErrGCSObjectNotFound
	}
	body, err := os.ReadFile(filepath.Join(s.objects, obj.File))
	return obj, body, err
}

func (s *Store) ListGCSObjects(bucket, prefix, after string, limit int) ([]GCSObject, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.GCSBuckets[bucket]
	if !ok {
		return nil, false, ErrGCSBucketNotFound
	}
	keys := []string{}
	for key := range b.Objects {
		if strings.HasPrefix(key, prefix) && key > after {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	truncated := limit > 0 && len(keys) > limit
	if truncated {
		keys = keys[:limit]
	}
	result := make([]GCSObject, 0, len(keys))
	for _, key := range keys {
		result = append(result, b.Objects[key])
	}
	return result, truncated, nil
}

func (s *Store) PatchGCSObject(bucket, name, contentType string, metadata map[string]string) (GCSObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.GCSBuckets[bucket]
	if !ok {
		return GCSObject{}, ErrGCSBucketNotFound
	}
	obj, ok := b.Objects[name]
	if !ok {
		return GCSObject{}, ErrGCSObjectNotFound
	}
	if contentType != "" {
		obj.ContentType = contentType
	}
	if metadata != nil {
		obj.Metadata = cloneStringMap(metadata)
	}
	obj.Metageneration++
	obj.UpdatedAt = s.now().UTC()
	b.Objects[name] = obj
	if err := s.saveLocked(); err != nil {
		return GCSObject{}, err
	}
	return obj, nil
}

func (s *Store) DeleteGCSObject(bucket, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.GCSBuckets[bucket]
	if !ok {
		return ErrGCSBucketNotFound
	}
	obj, ok := b.Objects[name]
	if !ok {
		return ErrGCSObjectNotFound
	}
	delete(b.Objects, name)
	if err := s.saveLocked(); err != nil {
		b.Objects[name] = obj
		return err
	}
	_ = os.Remove(filepath.Join(s.objects, obj.File))
	return nil
}

func (s *Store) CreatePubSubTopic(name string, labels map[string]string) (PubSubTopic, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.data.PubSubTopics[name]; ok {
		return *existing, nil
	}
	topic := &PubSubTopic{Name: name, Labels: cloneStringMap(labels), CreatedAt: s.now().UTC()}
	s.data.PubSubTopics[name] = topic
	if err := s.saveLocked(); err != nil {
		return PubSubTopic{}, err
	}
	return *topic, nil
}

func (s *Store) PubSubTopic(name string) (PubSubTopic, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data.PubSubTopics[name]
	if !ok {
		return PubSubTopic{}, ErrPubSubTopicNotFound
	}
	c := *t
	c.Labels = cloneStringMap(t.Labels)
	return c, nil
}
func (s *Store) ListPubSubTopics(projectPrefix string) []PubSubTopic {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []PubSubTopic{}
	for _, t := range s.data.PubSubTopics {
		if strings.HasPrefix(t.Name, projectPrefix) {
			c := *t
			c.Labels = cloneStringMap(t.Labels)
			result = append(result, c)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) DeletePubSubTopic(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.PubSubTopics[name]; !ok {
		return ErrPubSubTopicNotFound
	}
	delete(s.data.PubSubTopics, name)
	for _, sub := range s.data.PubSubSubscriptions {
		if sub.Topic == name {
			sub.Topic = "_deleted-topic_"
		}
	}
	return s.saveLocked()
}

func (s *Store) CreatePubSubSubscription(name, topic string, deadline int32, labels map[string]string, ordering bool) (PubSubSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.PubSubTopics[topic]; !ok {
		return PubSubSubscription{}, ErrPubSubTopicNotFound
	}
	if existing, ok := s.data.PubSubSubscriptions[name]; ok {
		return clonePubSubSubscription(existing), nil
	}
	if deadline == 0 {
		deadline = 10
	}
	sub := &PubSubSubscription{Name: name, Topic: topic, AckDeadlineSeconds: deadline, Labels: cloneStringMap(labels), EnableOrdering: ordering, CreatedAt: s.now().UTC(), Messages: []PubSubMessage{}}
	s.data.PubSubSubscriptions[name] = sub
	if err := s.saveLocked(); err != nil {
		return PubSubSubscription{}, err
	}
	return clonePubSubSubscription(sub), nil
}

func clonePubSubSubscription(sub *PubSubSubscription) PubSubSubscription {
	c := *sub
	c.Labels = cloneStringMap(sub.Labels)
	c.Messages = append([]PubSubMessage(nil), sub.Messages...)
	return c
}
func (s *Store) PubSubSubscription(name string) (PubSubSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data.PubSubSubscriptions[name]
	if !ok {
		return PubSubSubscription{}, ErrPubSubSubscriptionNotFound
	}
	return clonePubSubSubscription(sub), nil
}
func (s *Store) ListPubSubSubscriptions(projectPrefix string) []PubSubSubscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []PubSubSubscription{}
	for _, sub := range s.data.PubSubSubscriptions {
		if strings.HasPrefix(sub.Name, projectPrefix) {
			result = append(result, clonePubSubSubscription(sub))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
func (s *Store) DeletePubSubSubscription(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.PubSubSubscriptions[name]; !ok {
		return ErrPubSubSubscriptionNotFound
	}
	delete(s.data.PubSubSubscriptions, name)
	return s.saveLocked()
}

func (s *Store) PurgePubSubSubscription(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	subscription, ok := s.data.PubSubSubscriptions[name]
	if !ok {
		return ErrPubSubSubscriptionNotFound
	}
	previous := subscription.Messages
	subscription.Messages = []PubSubMessage{}
	if err := s.saveLocked(); err != nil {
		subscription.Messages = previous
		return err
	}
	return nil
}

func (s *Store) PublishPubSub(topic string, messages []PubSubMessage) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.PubSubTopics[topic]; !ok {
		return nil, ErrPubSubTopicNotFound
	}
	ids := make([]string, len(messages))
	now := s.now().UTC()
	for i := range messages {
		messages[i].MessageID = newID()
		messages[i].PublishTime = now
		messages[i].VisibleAt = now
		messages[i].Attributes = cloneStringMap(messages[i].Attributes)
		ids[i] = messages[i].MessageID
	}
	for _, sub := range s.data.PubSubSubscriptions {
		if sub.Topic == topic {
			for _, message := range messages {
				copy := message
				copy.Data = append([]byte(nil), message.Data...)
				copy.Attributes = cloneStringMap(message.Attributes)
				sub.Messages = append(sub.Messages, copy)
			}
		}
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Store) PullPubSub(subscription string, max int, deadline int32) ([]PubSubMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data.PubSubSubscriptions[subscription]
	if !ok {
		return nil, ErrPubSubSubscriptionNotFound
	}
	if sub.Topic == "_deleted-topic_" {
		return nil, ErrPubSubTopicNotFound
	}
	if max < 1 {
		max = 1
	}
	if deadline <= 0 {
		deadline = sub.AckDeadlineSeconds
	}
	now := s.now().UTC()
	result := []PubSubMessage{}
	kept := make([]PubSubMessage, 0, len(sub.Messages))
	changed := false
	for i := range sub.Messages {
		m := &sub.Messages[i]
		if !m.VisibleAt.After(now) && sub.DeadLetterTopic != "" && sub.MaxDeliveryAttempts > 0 && m.DeliveryAttempt >= sub.MaxDeliveryAttempts {
			forwarded := *m
			forwarded.MessageID = newID()
			forwarded.AckID = ""
			forwarded.PublishTime = now
			forwarded.VisibleAt = now
			forwarded.DeliveryAttempt = 0
			forwarded.Data = append([]byte(nil), m.Data...)
			forwarded.Attributes = cloneStringMap(m.Attributes)
			for _, target := range s.data.PubSubSubscriptions {
				if target.Topic == sub.DeadLetterTopic {
					target.Messages = append(target.Messages, forwarded)
				}
			}
			changed = true
			continue
		}
		if len(result) < max && !m.VisibleAt.After(now) {
			m.DeliveryAttempt++
			m.AckID = newID()
			m.VisibleAt = now.Add(time.Duration(deadline) * time.Second)
			copy := *m
			copy.Data = append([]byte(nil), m.Data...)
			copy.Attributes = cloneStringMap(m.Attributes)
			result = append(result, copy)
			changed = true
		}
		kept = append(kept, *m)
	}
	sub.Messages = kept
	if changed {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) AckPubSub(subscription string, ackIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data.PubSubSubscriptions[subscription]
	if !ok {
		return ErrPubSubSubscriptionNotFound
	}
	set := map[string]struct{}{}
	for _, id := range ackIDs {
		set[id] = struct{}{}
	}
	kept := sub.Messages[:0]
	for _, m := range sub.Messages {
		if _, acked := set[m.AckID]; !acked {
			kept = append(kept, m)
		}
	}
	sub.Messages = kept
	return s.saveLocked()
}

func (s *Store) ModifyPubSubAckDeadline(subscription string, ackIDs []string, seconds []int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.data.PubSubSubscriptions[subscription]
	if !ok {
		return ErrPubSubSubscriptionNotFound
	}
	deadlines := map[string]int32{}
	for i, id := range ackIDs {
		value := int32(0)
		if len(seconds) == 1 {
			value = seconds[0]
		} else if i < len(seconds) {
			value = seconds[i]
		}
		deadlines[id] = value
	}
	now := s.now().UTC()
	for i := range sub.Messages {
		if seconds, ok := deadlines[sub.Messages[i].AckID]; ok {
			sub.Messages[i].VisibleAt = now.Add(time.Duration(seconds) * time.Second)
		}
	}
	return s.saveLocked()
}

func (s *Store) PubSubTopicSubscriptions(topic string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []string{}
	for _, sub := range s.data.PubSubSubscriptions {
		if sub.Topic == topic {
			result = append(result, sub.Name)
		}
	}
	sort.Strings(result)
	return result
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for k, v := range input {
		result[k] = v
	}
	return result
}

func EncodePageToken(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
func DecodePageToken(value string) string {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return ""
	}
	return string(raw)
}
