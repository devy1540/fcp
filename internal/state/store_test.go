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

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	obj, body, err := reopened.GetObject("assets", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello" || obj.ContentType != "text/plain" || obj.Metadata["env"] != "test" {
		t.Fatalf("unexpected object after reopen: obj=%+v body=%q", obj, body)
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

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
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

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
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
