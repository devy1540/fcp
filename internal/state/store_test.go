package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestObjectPersistsAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("assets", "hello.txt", []byte("hello"), "text/plain", map[string]string{"env": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	obj, body, err := reopened.GetObject("assets", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello" || obj.ContentType != "text/plain" || obj.Metadata["env"] != "test" {
		t.Fatalf("unexpected object after reopen: obj=%+v body=%q", obj, body)
	}
}

func TestFailedSaveRollsBackObjectAndQueueMutations(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	original, err := store.PutObject("assets", "hello.txt", []byte("original"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateQueue("jobs", nil); err != nil {
		t.Fatal(err)
	}

	restoreStateFile := obstructStateFile(t, dir)
	if _, err := store.PutObject("assets", "hello.txt", []byte("must-not-commit"), "text/plain", nil); err == nil {
		t.Fatal("object write should fail while state.json is obstructed")
	}
	if _, err := store.SendMessage("jobs", "must-not-commit", nil, 0); err == nil {
		t.Fatal("queue write should fail while state.json is obstructed")
	}
	if _, err := store.RecordFCMMessage("test-project", json.RawMessage(`{"token":"must-not-commit"}`), false); err == nil {
		t.Fatal("FCM write should fail while state.json is obstructed")
	}
	if _, err := store.RecordVertexGeneration("test-project", "asia-northeast3", "test-model", "generateContent", 1, 0); err == nil {
		t.Fatal("Vertex write should fail while state.json is obstructed")
	}
	if err := store.DeleteObject("assets", "hello.txt"); err == nil {
		t.Fatal("object delete should fail while state.json is obstructed")
	}
	if err := store.Reset(); err == nil {
		t.Fatal("reset should fail while state.json is obstructed")
	}
	_, body, err := store.GetObject("assets", "hello.txt")
	if err != nil || string(body) != "original" {
		t.Fatalf("failed object mutation remained in memory: body=%q err=%v", body, err)
	}
	queue, err := store.Queue("jobs")
	if err != nil || len(queue.Messages) != 0 {
		t.Fatalf("failed queue mutation remained in memory: queue=%+v err=%v", queue, err)
	}
	if messages := store.ListFCMMessages("test-project"); len(messages) != 0 {
		t.Fatalf("failed FCM mutation remained in memory: %+v", messages)
	}
	if generations := store.ListVertexGenerations("test-project"); len(generations) != 0 {
		t.Fatalf("failed Vertex mutation remained in memory: %+v", generations)
	}
	restoreStateFile()

	// A later successful save used to persist both earlier failed mutations.
	if err := store.CreateBucket("later-success"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	object, body, err := reopened.GetObject("assets", "hello.txt")
	if err != nil || string(body) != "original" || object.File != original.File {
		t.Fatalf("failed object mutation persisted later: object=%+v body=%q err=%v", object, body, err)
	}
	queue, err = reopened.Queue("jobs")
	if err != nil || len(queue.Messages) != 0 {
		t.Fatalf("failed queue mutation persisted later: queue=%+v err=%v", queue, err)
	}
	if messages := reopened.ListFCMMessages("test-project"); len(messages) != 0 {
		t.Fatalf("failed FCM mutation persisted later: %+v", messages)
	}
	if generations := reopened.ListVertexGenerations("test-project"); len(generations) != 0 {
		t.Fatalf("failed Vertex mutation persisted later: %+v", generations)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != original.File {
		t.Fatalf("failed object generation was not cleaned up: %+v", entries)
	}
}

func TestFailedSaveRollsBackServiceMutations(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	schema := []DynamoKeySchemaElement{{AttributeName: "pk", KeyType: "HASH"}}
	definitions := []DynamoAttributeDefinition{{AttributeName: "pk", AttributeType: "S"}}
	if _, err := store.CreateDynamoTable("records", schema, definitions, "PAY_PER_REQUEST"); err != nil {
		t.Fatal(err)
	}
	secretName := "projects/test/secrets/backend"
	if _, err := store.CreateSecret(secretName, map[string]string{"env": "original"}); err != nil {
		t.Fatal(err)
	}
	topicName := "projects/test/topics/events"
	subscriptionName := "projects/test/subscriptions/worker"
	if _, err := store.CreatePubSubTopic(topicName, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePubSubSubscription(subscriptionName, topicName, 10, nil, false); err != nil {
		t.Fatal(err)
	}
	keyRingName := "projects/test/locations/global/keyRings/local"
	if _, err := store.CreateKMSKeyRing(keyRingName); err != nil {
		t.Fatal(err)
	}
	keyName := keyRingName + "/cryptoKeys/data"
	if _, err := store.CreateKMSCryptoKey(KMSCryptoKey{Name: keyName}); err != nil {
		t.Fatal(err)
	}

	restoreStateFile := obstructStateFile(t, dir)
	if _, err := store.UpdateSecretLabels(secretName, map[string]string{"env": "failed"}); err == nil {
		t.Fatal("secret label update should fail while state.json is obstructed")
	}
	if _, err := store.UpdatePubSubSubscription(subscriptionName, 60, map[string]string{"env": "failed"}, "", 0, true, true, false); err == nil {
		t.Fatal("subscription update should fail while state.json is obstructed")
	}
	newItem := dynamoTestItem("must-not-commit", "", "failed")
	if err := store.DynamoTransactWrite([]DynamoWriteOperation{{Kind: "put", Table: "records", Item: newItem}}); err == nil {
		t.Fatal("DynamoDB transaction should fail while state.json is obstructed")
	}
	if _, err := store.AddKMSKeyVersion(keyName, "GOOGLE_SYMMETRIC_ENCRYPTION", []byte("must-not-commit")); err == nil {
		t.Fatal("KMS version creation should fail while state.json is obstructed")
	}
	accountName := "projects/-/serviceAccounts/fcp@test.iam.gserviceaccount.com"
	if _, err := store.IAMServiceAccount(accountName, func() ([]byte, error) {
		return []byte("must-not-commit"), nil
	}); err == nil {
		t.Fatal("IAM account creation should fail while state.json is obstructed")
	}
	documentName := "projects/test/databases/(default)/documents/config/failed"
	if err := store.MutateFirestore(func(documents map[string]*FirestoreDocument, now time.Time) error {
		documents[documentName] = &FirestoreDocument{Name: documentName, CreateTime: now, UpdateTime: now}
		return nil
	}); err == nil {
		t.Fatal("Firestore mutation should fail while state.json is obstructed")
	}

	assertServiceMutationsRolledBack(t, store, secretName, subscriptionName, keyName, accountName, documentName)
	restoreStateFile()
	if err := store.CreateBucket("later-success"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertServiceMutationsRolledBack(t, reopened, secretName, subscriptionName, keyName, accountName, documentName)
}

func assertServiceMutationsRolledBack(t *testing.T, store *Store, secretName, subscriptionName, keyName, accountName, documentName string) {
	t.Helper()
	secret, err := store.Secret(secretName)
	if err != nil || secret.Labels["env"] != "original" {
		t.Fatalf("failed secret mutation remained: secret=%+v err=%v", secret, err)
	}
	subscription, err := store.PubSubSubscription(subscriptionName)
	if err != nil || subscription.AckDeadlineSeconds != 10 || len(subscription.Labels) != 0 {
		t.Fatalf("failed subscription mutation remained: subscription=%+v err=%v", subscription, err)
	}
	if _, exists, err := store.DynamoGetItem("records", dynamoTestKey("must-not-commit", "")); err != nil || exists {
		t.Fatalf("failed DynamoDB transaction remained: exists=%v err=%v", exists, err)
	}
	key, err := store.KMSCryptoKey(keyName)
	if err != nil || len(key.Versions) != 0 || key.PrimaryVersion != 0 {
		t.Fatalf("failed KMS mutation remained: key=%+v err=%v", key, err)
	}
	if _, err := store.ExistingIAMServiceAccount(accountName); !errors.Is(err, ErrIAMServiceAccountNotFound) {
		t.Fatalf("failed IAM mutation remained: %v", err)
	}
	if _, err := store.FirestoreDocument(documentName); !errors.Is(err, ErrFirestoreDocumentNotFound) {
		t.Fatalf("failed Firestore mutation remained: %v", err)
	}
}

func TestOpenRejectsMissingAndTamperedObjectFiles(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper func(string) error
	}{
		{name: "missing", tamper: os.Remove},
		{name: "same-size tamper", tamper: func(path string) error {
			return os.WriteFile(path, []byte("HELLO"), 0o600)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CreateBucket("assets"); err != nil {
				t.Fatal(err)
			}
			object, err := store.PutObject("assets", "hello.txt", []byte("hello"), "text/plain", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if err := test.tamper(filepath.Join(dir, "objects", object.File)); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir); !errors.Is(err, ErrStateCorrupt) {
				t.Fatalf("Open should reject corrupt object state: %v", err)
			}
		})
	}
}

func TestOpenRejectsMalformedUnsafeAndOversizedState(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "invalid JSON", raw: `{"buckets":`},
		{name: "null resource", raw: `{"buckets":{"assets":null},"queues":{}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(test.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir); !errors.Is(err, ErrStateCorrupt) {
				t.Fatalf("Open should reject malformed state: %v", err)
			}
		})
	}

	t.Run("oversized", func(t *testing.T) {
		dir := t.TempDir()
		file, err := os.OpenFile(filepath.Join(dir, "state.json"), os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxPersistentStateSize + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("Open should reject oversized state: %v", err)
		}
	})

	t.Run("state symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "state-target.json")
		if err := os.WriteFile(target, []byte(`{"buckets":{},"queues":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "state.json")); err != nil {
			t.Skipf("symlink is not supported: %v", err)
		}
		if _, err := Open(dir); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("Open should reject state symlink: %v", err)
		}
	})

	t.Run("object directory symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(dir, "objects")); err != nil {
			t.Skipf("symlink is not supported: %v", err)
		}
		if _, err := Open(dir); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("Open should reject object directory symlink: %v", err)
		}
	})
}

func TestOpenCleansUnreferencedObjectFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("assets", "hello.txt", []byte("hello"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, "objects", "unreferenced")
	if err := os.WriteFile(orphan, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced object file still exists: %v", err)
	}
}

func TestOpenAcceptsLegacyObjectMetadataWithoutSHA256(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("assets", "hello.txt", []byte("legacy"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var data snapshot
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	object := data.Buckets["assets"].Objects["hello.txt"]
	object.SHA256 = ""
	data.Buckets["assets"].Objects["hello.txt"] = object
	raw, err = encodeSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, body, err := reopened.GetObject("assets", "hello.txt")
	if err != nil || string(body) != "legacy" {
		t.Fatalf("legacy object was not readable: body=%q err=%v", body, err)
	}
}

func obstructStateFile(t *testing.T, dir string) func() {
	t.Helper()
	statePath := filepath.Join(dir, "state.json")
	backupPath := filepath.Join(dir, "state.test-backup.json")
	if err := os.Rename(statePath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.Remove(statePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(backupPath, statePath); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMultipartUploadPersistsAcrossOpenCompletesAndAborts(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	upload, err := store.CreateMultipartUpload("assets", "large.bin", "application/octet-stream", map[string]string{"source": "test"})
	if err != nil {
		t.Fatal(err)
	}
	part, err := store.UploadMultipartPart("assets", "large.bin", upload.ID, 1, []byte("persistent part"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	storedUpload, parts, err := reopened.ListMultipartParts("assets", "large.bin", upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedUpload.ContentType != "application/octet-stream" || storedUpload.Metadata["source"] != "test" || len(parts) != 1 || parts[0].ETag != part.ETag {
		t.Fatalf("unexpected multipart upload after reopen: upload=%+v parts=%+v", storedUpload, parts)
	}
	obj, err := reopened.CompleteMultipartUpload("assets", "large.bin", upload.ID, []CompletedMultipartPart{{PartNumber: 1, ETag: `"` + part.ETag + `"`}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(obj.ETag, "-1") || obj.Metadata["source"] != "test" {
		t.Fatalf("unexpected completed object: %+v", obj)
	}
	_, body, err := reopened.GetObject("assets", "large.bin")
	if err != nil || string(body) != "persistent part" {
		t.Fatalf("unexpected completed body: body=%q err=%v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "objects", part.File)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed part file still exists: %v", err)
	}

	aborted, err := reopened.CreateMultipartUpload("assets", "aborted.bin", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	abortedPart, err := reopened.UploadMultipartPart("assets", "aborted.bin", aborted.ID, 1, []byte("discarded"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.AbortMultipartUpload("assets", "aborted.bin", aborted.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.ListMultipartParts("assets", "aborted.bin", aborted.ID); !errors.Is(err, ErrMultipartUploadNotFound) {
		t.Fatalf("aborted upload still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "objects", abortedPart.File)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aborted part file still exists: %v", err)
	}
}

func TestQueueVisibilityAndReceipt(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if _, err := store.CreateQueue("jobs", map[string]string{"VisibilityTimeout": "30"}); err != nil {
		t.Fatal(err)
	}
	sent, err := store.SendMessage("jobs", "hello", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.ReceiveMessages("jobs", 1, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].MessageID != sent.MessageID || messages[0].ReceiptHandle == "" {
		t.Fatalf("unexpected receive: %+v", messages)
	}
	if again, err := store.ReceiveMessages("jobs", 1, -1); err != nil || len(again) != 0 {
		t.Fatalf("message should be invisible: messages=%+v err=%v", again, err)
	}
	now = now.Add(31 * time.Second)
	again, err := store.ReceiveMessages("jobs", 1, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].ReceiveCount != 2 {
		t.Fatalf("message should be visible again: %+v", again)
	}
	if err := store.DeleteMessage("jobs", again[0].ReceiptHandle); err != nil {
		t.Fatal(err)
	}
	attrs, err := store.QueueAttributes("jobs")
	if err != nil {
		t.Fatal(err)
	}
	if attrs["ApproximateNumberOfMessages"] != "0" {
		t.Fatalf("message was not deleted: %+v", attrs)
	}
}

func TestQueueRedrivePolicyMovesMessageAfterMaxReceives(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if _, err := store.CreateQueue("jobs-dlq", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateQueue("jobs", map[string]string{"VisibilityTimeout": "0"}); err != nil {
		t.Fatal(err)
	}
	policy := `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:jobs-dlq","maxReceiveCount":"2"}`
	if err := store.SetQueueAttributes("jobs", map[string]string{"RedrivePolicy": policy}); err != nil {
		t.Fatal(err)
	}
	sent, err := store.SendMessage("jobs", "poison", map[string]MessageAttribute{"source": {DataType: "String", StringValue: "unit-test"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for expectedCount := 1; expectedCount <= 2; expectedCount++ {
		messages, err := store.ReceiveMessages("jobs", 1, -1)
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != 1 || messages[0].MessageID != sent.MessageID || messages[0].ReceiveCount != expectedCount {
			t.Fatalf("unexpected source delivery %d: %+v", expectedCount, messages)
		}
	}
	if messages, err := store.ReceiveMessages("jobs", 1, -1); err != nil || len(messages) != 0 {
		t.Fatalf("message should be moved instead of delivered again: messages=%+v err=%v", messages, err)
	}
	source, err := store.Queue("jobs")
	if err != nil || len(source.Messages) != 0 {
		t.Fatalf("source queue still has redriven message: queue=%+v err=%v", source, err)
	}
	dlq, err := store.Queue("jobs-dlq")
	if err != nil || len(dlq.Messages) != 1 || dlq.Messages[0].Body != "poison" || dlq.Messages[0].MessageID != sent.MessageID || dlq.Messages[0].MessageAttributes["source"].StringValue != "unit-test" {
		t.Fatalf("unexpected dead-letter queue: queue=%+v err=%v", dlq, err)
	}
}

func TestQueueRejectsInvalidRedrivePolicy(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateQueue("jobs", nil); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{
		`not-json`,
		`{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:missing","maxReceiveCount":"2"}`,
		`{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:jobs","maxReceiveCount":"2"}`,
		`{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:jobs","maxReceiveCount":"0"}`,
	} {
		if err := store.SetQueueAttributes("jobs", map[string]string{"RedrivePolicy": policy}); !errors.Is(err, ErrInvalidQueueAttribute) {
			t.Fatalf("policy %q should be rejected: %v", policy, err)
		}
	}
}

func TestFIFOQueueDeduplicationAndOrderingPersistAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if _, err := store.CreateQueue("orders", map[string]string{"FifoQueue": "true"}); !errors.Is(err, ErrInvalidQueueAttribute) {
		t.Fatalf("FIFO queue without .fifo suffix should fail: %v", err)
	}
	if _, err := store.CreateQueue("orders.fifo", map[string]string{"FifoQueue": "true", "VisibilityTimeout": "30"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SendMessageWithOptions("orders.fifo", "missing-group", nil, 0, SendMessageOptions{MessageDeduplicationID: "missing"}); !errors.Is(err, ErrMissingMessageParameter) {
		t.Fatalf("FIFO send without group should fail: %v", err)
	}
	first, err := store.SendMessageWithOptions("orders.fifo", "group-a-1", nil, 0, SendMessageOptions{MessageGroupID: "group-a", MessageDeduplicationID: "a-1"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.SendMessageWithOptions("orders.fifo", "different-body", nil, 0, SendMessageOptions{MessageGroupID: "group-a", MessageDeduplicationID: "a-1"})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.MessageID != first.MessageID || duplicate.SequenceNumber != first.SequenceNumber {
		t.Fatalf("deduplicated response changed identity: first=%+v duplicate=%+v", first, duplicate)
	}
	second, err := store.SendMessageWithOptions("orders.fifo", "group-a-2", nil, 0, SendMessageOptions{MessageGroupID: "group-a", MessageDeduplicationID: "a-2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SendMessageWithOptions("orders.fifo", "group-b-1", nil, 0, SendMessageOptions{MessageGroupID: "group-b", MessageDeduplicationID: "b-1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return now }
	duplicateAfterOpen, err := reopened.SendMessageWithOptions("orders.fifo", "after-open", nil, 0, SendMessageOptions{MessageGroupID: "group-a", MessageDeduplicationID: "a-1"})
	if err != nil {
		t.Fatal(err)
	}
	if duplicateAfterOpen.MessageID != first.MessageID {
		t.Fatalf("deduplication did not persist: %+v", duplicateAfterOpen)
	}
	firstDelivery, err := reopened.ReceiveMessages("orders.fifo", 1, -1)
	if err != nil || len(firstDelivery) != 1 || firstDelivery[0].MessageID != first.MessageID {
		t.Fatalf("unexpected first FIFO delivery: messages=%+v err=%v", firstDelivery, err)
	}
	otherGroup, err := reopened.ReceiveMessages("orders.fifo", 1, -1)
	if err != nil || len(otherGroup) != 1 || otherGroup[0].MessageGroupID != "group-b" {
		t.Fatalf("in-flight group did not allow another group: messages=%+v err=%v", otherGroup, err)
	}
	if err := reopened.ChangeVisibility("orders.fifo", firstDelivery[0].ReceiptHandle, 0); err != nil {
		t.Fatal(err)
	}
	redelivery, err := reopened.ReceiveMessages("orders.fifo", 1, -1)
	if err != nil || len(redelivery) != 1 || redelivery[0].MessageID != first.MessageID {
		t.Fatalf("FIFO head was not redelivered first: messages=%+v err=%v", redelivery, err)
	}
	if err := reopened.DeleteMessage("orders.fifo", redelivery[0].ReceiptHandle); err != nil {
		t.Fatal(err)
	}
	next, err := reopened.ReceiveMessages("orders.fifo", 1, -1)
	if err != nil || len(next) != 1 || next[0].MessageID != second.MessageID {
		t.Fatalf("second group-a message was not released: messages=%+v err=%v", next, err)
	}

	now = now.Add(5*time.Minute + time.Second)
	afterWindow, err := reopened.SendMessageWithOptions("orders.fifo", "new-after-window", nil, 0, SendMessageOptions{MessageGroupID: "group-a", MessageDeduplicationID: "a-1"})
	if err != nil {
		t.Fatal(err)
	}
	if afterWindow.MessageID == first.MessageID || afterWindow.SequenceNumber == first.SequenceNumber {
		t.Fatalf("expired deduplication ID was not accepted as new: %+v", afterWindow)
	}
}

func TestFIFOQueueRedrivePreservesGroupAndUsesMessageIDForDeduplication(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if _, err := store.CreateQueue("orders-dlq.fifo", map[string]string{"FifoQueue": "true"}); err != nil {
		t.Fatal(err)
	}
	policy := `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:orders-dlq.fifo","maxReceiveCount":"1"}`
	if _, err := store.CreateQueue("orders.fifo", map[string]string{"FifoQueue": "true", "VisibilityTimeout": "0", "RedrivePolicy": policy}); err != nil {
		t.Fatal(err)
	}
	sent, err := store.SendMessageWithOptions("orders.fifo", "poison", nil, 0, SendMessageOptions{MessageGroupID: "tenant-1", MessageDeduplicationID: "original-dedup"})
	if err != nil {
		t.Fatal(err)
	}
	if messages, err := store.ReceiveMessages("orders.fifo", 1, -1); err != nil || len(messages) != 1 {
		t.Fatalf("unexpected source delivery: messages=%+v err=%v", messages, err)
	}
	if messages, err := store.ReceiveMessages("orders.fifo", 1, -1); err != nil || len(messages) != 0 {
		t.Fatalf("redrive should not return a second source delivery: messages=%+v err=%v", messages, err)
	}
	dlq, err := store.Queue("orders-dlq.fifo")
	if err != nil || len(dlq.Messages) != 1 {
		t.Fatalf("unexpected FIFO DLQ: queue=%+v err=%v", dlq, err)
	}
	moved := dlq.Messages[0]
	if moved.MessageID != sent.MessageID || moved.MessageGroupID != "tenant-1" || moved.MessageDeduplicationID != sent.MessageID || moved.SequenceNumber == "" {
		t.Fatalf("FIFO redrive fields are incorrect: %+v", moved)
	}
}

func TestS3NotificationEnqueuesEvent(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("uploads"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateQueue("events", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNotifications("uploads", []Notification{{ID: "images", QueueARN: "arn:aws:sqs:us-east-1:000000000000:events", Events: []string{"s3:ObjectCreated:*"}, Prefix: "images/"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("uploads", "ignored.txt", []byte("no"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("uploads", "images/cat.jpg", []byte("image"), "image/jpeg", nil); err != nil {
		t.Fatal(err)
	}
	messages, err := store.ReceiveMessages("events", 10, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one filtered event, got %d", len(messages))
	}
	var event struct {
		Records []struct {
			EventName string `json:"eventName"`
			S3        struct {
				Object struct {
					Key string `json:"key"`
				} `json:"object"`
			} `json:"s3"`
		} `json:"Records"`
	}
	if err := json.Unmarshal([]byte(messages[0].Body), &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Records) != 1 || event.Records[0].EventName != "ObjectCreated:Put" || event.Records[0].S3.Object.Key != "images/cat.jpg" {
		t.Fatalf("unexpected event: %+v", event)
	}
}
