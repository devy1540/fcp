package state

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestBucketQueueCaptureAndResetEdges(t *testing.T) {
	store := openEdgeStore(t)
	if store.HasBucket("assets") {
		t.Fatal("bucket should not exist before creation")
	}
	if err := store.DeleteBucket("missing"); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("missing bucket delete error=%v", err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	if !store.HasBucket("assets") {
		t.Fatal("created bucket was not found")
	}
	if _, err := store.PutObject("assets", "one", []byte("body"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBucket("assets"); !errors.Is(err, ErrBucketNotEmpty) {
		t.Fatalf("non-empty bucket delete error=%v", err)
	}
	if err := store.DeleteObject("assets", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMultipartUpload("assets", "large", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBucket("assets"); !errors.Is(err, ErrBucketNotEmpty) {
		t.Fatalf("bucket with multipart upload delete error=%v", err)
	}

	if _, err := store.Notifications("missing"); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("missing notification bucket error=%v", err)
	}
	if err := store.SetNotifications("assets", []Notification{{QueueARN: "arn:aws:sqs:us-east-1:000000000000:missing"}}); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("missing notification queue error=%v", err)
	}
	if _, err := store.CreateQueue("events", nil); err != nil {
		t.Fatal(err)
	}
	notifications := []Notification{{ID: "one", QueueARN: "arn:aws:sqs:us-east-1:000000000000:events", Events: []string{"s3:ObjectCreated:*"}}}
	if err := store.SetNotifications("assets", notifications); err != nil {
		t.Fatal(err)
	}
	gotNotifications, err := store.Notifications("assets")
	if err != nil || len(gotNotifications) != 1 {
		t.Fatalf("unexpected notifications: %+v err=%v", gotNotifications, err)
	}
	gotNotifications[0].ID = "changed"
	again, _ := store.Notifications("assets")
	if again[0].ID != "one" {
		t.Fatal("notifications result should not alias stored slice")
	}

	if err := store.DeleteQueue("missing"); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("missing queue delete error=%v", err)
	}
	if err := store.DeleteQueue("events"); err != nil {
		t.Fatal(err)
	}
	if AccountID() != defaultAccountID {
		t.Fatalf("unexpected local account ID: %q", AccountID())
	}

	if _, err := store.RecordFCMMessage("one", json.RawMessage(`{"token":"one"}`), false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordFCMMessage("two", json.RawMessage(`{"token":"two"}`), true); err != nil {
		t.Fatal(err)
	}
	if got := store.ListFCMMessages("ONE"); len(got) != 1 || got[0].Project != "one" {
		t.Fatalf("unexpected filtered FCM messages: %+v", got)
	}
	if err := store.ClearFCMMessages(); err != nil || len(store.ListFCMMessages("")) != 0 {
		t.Fatalf("FCM messages were not cleared: err=%v", err)
	}
	if _, err := store.RecordVertexGeneration("one", "global", "model", "generateContent", 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordVertexGeneration("two", "global", "model", "generateContent", 1, 0); err != nil {
		t.Fatal(err)
	}
	if got := store.ListVertexGenerations("ONE"); len(got) != 1 || got[0].Project != "one" {
		t.Fatalf("unexpected filtered Vertex generations: %+v", got)
	}
	if err := store.ClearVertexGenerations(); err != nil || len(store.ListVertexGenerations("")) != 0 {
		t.Fatalf("Vertex generations were not cleared: err=%v", err)
	}

	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if store.HasBucket("assets") || len(store.ListQueues("")) != 0 {
		t.Fatal("full reset did not clear resources")
	}
}

func TestQueueAttributeValidationAndHelpers(t *testing.T) {
	for _, test := range []struct {
		name  string
		queue string
		attrs map[string]string
	}{
		{name: "invalid bool", queue: "jobs", attrs: map[string]string{"FifoQueue": "maybe"}},
		{name: "fifo suffix without flag", queue: "jobs.fifo"},
		{name: "fifo flag without suffix", queue: "jobs", attrs: map[string]string{"FifoQueue": "true"}},
		{name: "content dedup on standard", queue: "jobs", attrs: map[string]string{"ContentBasedDeduplication": "true"}},
		{name: "bad content dedup bool", queue: "jobs.fifo", attrs: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "maybe"}},
		{name: "bad dedup scope", queue: "jobs.fifo", attrs: map[string]string{"FifoQueue": "true", "DeduplicationScope": "bad"}},
		{name: "bad throughput", queue: "jobs.fifo", attrs: map[string]string{"FifoQueue": "true", "FifoThroughputLimit": "bad"}},
		{name: "throughput scope mismatch", queue: "jobs.fifo", attrs: map[string]string{"FifoQueue": "true", "FifoThroughputLimit": "perMessageGroupId"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateQueueCreation(test.queue, test.attrs); !errors.Is(err, ErrInvalidQueueAttribute) {
				t.Fatalf("expected invalid queue attribute: %v", err)
			}
		})
	}
	if err := validateQueueCreation("jobs.fifo", map[string]string{
		"FifoQueue":                 "true",
		"DeduplicationScope":        "messageGroup",
		"FifoThroughputLimit":       "perMessageGroupId",
		"ContentBasedDeduplication": "true",
	}); err != nil {
		t.Fatalf("valid FIFO attributes failed: %v", err)
	}
	queue := &Queue{Name: "jobs.fifo", Attributes: map[string]string{"FifoQueue": "true"}}
	if err := validateQueueAttributeMutation(queue, map[string]string{"FifoQueue": "false"}); !errors.Is(err, ErrInvalidQueueAttribute) {
		t.Fatalf("FifoQueue mutation should fail: %v", err)
	}
	if !queueIsFIFO(queue) || queueIsFIFO(nil) {
		t.Fatal("FIFO queue detection is incorrect")
	}
	if validFIFOIdentifier("") || validFIFOIdentifier(strings.Repeat("a", 129)) || validFIFOIdentifier("has space") || !validFIFOIdentifier("group-1") {
		t.Fatal("FIFO identifier validation is incorrect")
	}
	if got := fifoDeduplicationKey(queue, "group", "dedup"); got != "dedup" {
		t.Fatalf("queue-scoped deduplication key=%q", got)
	}
	queue.Attributes["DeduplicationScope"] = "messageGroup"
	if got := fifoDeduplicationKey(queue, "group", "dedup"); got != "group\x00dedup" {
		t.Fatalf("message-group deduplication key=%q", got)
	}
	if value, err := queueAttributeBool(map[string]string{}, "flag", true); err != nil || !value {
		t.Fatalf("queue bool fallback failed: value=%v err=%v", value, err)
	}
	if intValue("bad", 7) != 7 || intValue("9", 7) != 9 {
		t.Fatal("integer fallback parsing is incorrect")
	}
}

func TestDynamoAttributeSerializationKeysAndValidation(t *testing.T) {
	text := "text"
	boolean := false
	nullValue := true
	number := "1"
	values := []DynamoAttributeValue{
		{B: &text},
		{BOOL: &boolean},
		{BS: []string{"a"}},
		{L: []DynamoAttributeValue{{S: &text}}},
		{M: map[string]DynamoAttributeValue{"value": {S: &text}}},
		{N: &number},
		{NS: []string{"1"}},
		{NULL: &nullValue},
		{S: &text},
		{SS: []string{"a"}},
	}
	for index, value := range values {
		raw, err := json.Marshal(value)
		if err != nil || string(raw) == "{}" {
			t.Fatalf("attribute %d serialization failed: raw=%s err=%v", index, raw, err)
		}
	}
	if _, err := json.Marshal(DynamoAttributeValue{}); !errors.Is(err, ErrDynamoValidation) {
		t.Fatalf("empty attribute should fail: %v", err)
	}
	if got, err := dynamoScalarKey(DynamoAttributeValue{S: &text}); err != nil || !strings.HasPrefix(got, "S\x00") {
		t.Fatalf("string key failed: %q err=%v", got, err)
	}
	if got, err := dynamoScalarKey(DynamoAttributeValue{N: &number}); err != nil || !strings.HasPrefix(got, "N\x00") {
		t.Fatalf("number key failed: %q err=%v", got, err)
	}
	if got, err := dynamoScalarKey(DynamoAttributeValue{B: &text}); err != nil || !strings.HasPrefix(got, "B\x00") {
		t.Fatalf("binary key failed: %q err=%v", got, err)
	}
	if _, err := dynamoScalarKey(DynamoAttributeValue{BOOL: &boolean}); !errors.Is(err, ErrDynamoValidation) {
		t.Fatalf("non-scalar key should fail: %v", err)
	}

	validKey := []DynamoKeySchemaElement{{AttributeName: "pk", KeyType: "HASH"}}
	validDefinition := []DynamoAttributeDefinition{{AttributeName: "pk", AttributeType: "S"}}
	if err := validateDynamoTable("valid-table", validKey, validDefinition); err != nil {
		t.Fatalf("valid table rejected: %v", err)
	}
	for _, test := range []struct {
		name        string
		table       string
		key         []DynamoKeySchemaElement
		definitions []DynamoAttributeDefinition
	}{
		{name: "bad table name", table: "x", key: validKey, definitions: validDefinition},
		{name: "missing key", table: "valid-table", definitions: validDefinition},
		{name: "too many keys", table: "valid-table", key: []DynamoKeySchemaElement{{AttributeName: "a", KeyType: "HASH"}, {AttributeName: "b", KeyType: "RANGE"}, {AttributeName: "c", KeyType: "RANGE"}}, definitions: []DynamoAttributeDefinition{{AttributeName: "a", AttributeType: "S"}, {AttributeName: "b", AttributeType: "S"}, {AttributeName: "c", AttributeType: "S"}}},
		{name: "invalid definition type", table: "valid-table", key: validKey, definitions: []DynamoAttributeDefinition{{AttributeName: "pk", AttributeType: "BOOL"}}},
		{name: "duplicate definition", table: "valid-table", key: validKey, definitions: []DynamoAttributeDefinition{{AttributeName: "pk", AttributeType: "S"}, {AttributeName: "pk", AttributeType: "S"}}},
		{name: "undefined key", table: "valid-table", key: []DynamoKeySchemaElement{{AttributeName: "missing", KeyType: "HASH"}}, definitions: validDefinition},
		{name: "invalid key type", table: "valid-table", key: []DynamoKeySchemaElement{{AttributeName: "pk", KeyType: "BAD"}}, definitions: validDefinition},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateDynamoTable(test.table, test.key, test.definitions); !errors.Is(err, ErrDynamoValidation) {
				t.Fatalf("expected Dynamo validation error: %v", err)
			}
		})
	}
}

func TestKMSKeyRingLookupEdges(t *testing.T) {
	store := openEdgeStore(t)
	if _, err := store.KMSKeyRing("missing"); !errors.Is(err, ErrKMSKeyRingNotFound) {
		t.Fatalf("missing key ring error=%v", err)
	}
	name := "projects/test/locations/global/keyRings/main"
	created, err := store.CreateKMSKeyRing(name)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.KMSKeyRing(name)
	if err != nil || got != created {
		t.Fatalf("unexpected key ring: got=%+v created=%+v err=%v", got, created, err)
	}
}

func openEdgeStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
