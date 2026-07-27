package state

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestQueueSendFIFOAndMutationErrorEdges(t *testing.T) {
	store := openEdgeStore(t)
	if _, err := store.SendMessageWithOptions("missing", "body", nil, 0, SendMessageOptions{}); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("missing queue send error=%v", err)
	}
	if _, err := store.CreateQueue("standard", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SendMessageWithOptions("standard", "body", nil, 0, SendMessageOptions{MessageDeduplicationID: "dedup"}); !errors.Is(err, ErrInvalidMessageParameter) {
		t.Fatalf("standard queue deduplication error=%v", err)
	}
	if _, err := store.SendMessage("standard", "body", nil, 901); !errors.Is(err, ErrInvalidMessageParameter) {
		t.Fatalf("invalid message delay error=%v", err)
	}
	if err := store.SetQueueAttributes("missing", nil); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("missing queue attribute mutation error=%v", err)
	}
	if err := store.SetQueueAttributes("standard", map[string]string{"ContentBasedDeduplication": "true"}); !errors.Is(err, ErrInvalidQueueAttribute) {
		t.Fatalf("standard content deduplication error=%v", err)
	}

	if _, err := store.CreateQueue("ordered.fifo", map[string]string{"FifoQueue": "true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SendMessageWithOptions("ordered.fifo", "body", nil, 1, SendMessageOptions{
		MessageGroupID: "group", MessageDeduplicationID: "dedup", DelaySpecified: true,
	}); !errors.Is(err, ErrInvalidMessageParameter) {
		t.Fatalf("FIFO individual delay error=%v", err)
	}
	if _, err := store.SendMessageWithOptions("ordered.fifo", "body", nil, 0, SendMessageOptions{MessageDeduplicationID: "dedup"}); !errors.Is(err, ErrMissingMessageParameter) {
		t.Fatalf("missing FIFO group error=%v", err)
	}
	if _, err := store.SendMessageWithOptions("ordered.fifo", "body", nil, 0, SendMessageOptions{MessageGroupID: "has space", MessageDeduplicationID: "dedup"}); !errors.Is(err, ErrInvalidMessageParameter) {
		t.Fatalf("invalid FIFO group error=%v", err)
	}
	if _, err := store.SendMessageWithOptions("ordered.fifo", "body", nil, 0, SendMessageOptions{MessageGroupID: "group"}); !errors.Is(err, ErrMissingMessageParameter) {
		t.Fatalf("missing FIFO deduplication ID error=%v", err)
	}
	if _, err := store.SendMessageWithOptions("ordered.fifo", "body", nil, 0, SendMessageOptions{MessageGroupID: "group", MessageDeduplicationID: "has space"}); !errors.Is(err, ErrInvalidMessageParameter) {
		t.Fatalf("invalid FIFO deduplication ID error=%v", err)
	}
	if err := store.SetQueueAttributes("ordered.fifo", map[string]string{"ContentBasedDeduplication": "true"}); err != nil {
		t.Fatal(err)
	}
	first, err := store.SendMessageWithOptions("ordered.fifo", "same", nil, 0, SendMessageOptions{MessageGroupID: "group"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.SendMessageWithOptions("ordered.fifo", "same", nil, 0, SendMessageOptions{MessageGroupID: "group"})
	if err != nil || duplicate.MessageID != first.MessageID || duplicate.SequenceNumber != first.SequenceNumber {
		t.Fatalf("content-based duplicate was not deduplicated: first=%+v duplicate=%+v err=%v", first, duplicate, err)
	}

	store.mu.Lock()
	store.data.Queues["ordered.fifo"].Deduplication["expired"] = DeduplicationRecord{ExpiresAt: time.Time{}}
	store.mu.Unlock()
	if _, err := store.SendMessageWithOptions("ordered.fifo", "new", nil, 0, SendMessageOptions{MessageGroupID: "group"}); err != nil {
		t.Fatal(err)
	}
}

func TestQueueRedriveParsingRemovalAndReceiveBounds(t *testing.T) {
	store := openEdgeStore(t)
	if _, err := store.CreateQueue("source", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateQueue("dead", nil); err != nil {
		t.Fatal(err)
	}
	policy := `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:dead","maxReceiveCount":"2"}`
	if err := store.SetQueueAttributes("source", map[string]string{"RedrivePolicy": policy}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetQueueAttributes("source", map[string]string{"RedrivePolicy": " "}); err != nil {
		t.Fatal(err)
	}
	attributes, err := store.QueueAttributes("source")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := attributes["RedrivePolicy"]; exists {
		t.Fatal("blank redrive policy should remove the attribute")
	}

	for _, raw := range []string{
		`{`,
		`{}`,
		`{"deadLetterTargetArn":"arn","maxReceiveCount":true}`,
		`{"deadLetterTargetArn":"arn","maxReceiveCount":"bad"}`,
		`{"deadLetterTargetArn":"arn","maxReceiveCount":0}`,
	} {
		if _, err := parseRedrivePolicy(raw); err == nil {
			t.Fatalf("invalid redrive policy accepted: %s", raw)
		}
	}
	if got := queueNameFromARN("invalid"); got != "" {
		t.Fatalf("invalid queue ARN resolved: %q", got)
	}
	if got := queueNameFromARN("arn:aws:sqs:us-east-1:000000000000:dead"); got != "dead" {
		t.Fatalf("queue ARN name=%q", got)
	}

	if messages, err := store.ReceiveMessages("source", 0, -1); err != nil || len(messages) != 0 {
		t.Fatalf("empty receive failed: messages=%+v err=%v", messages, err)
	}
	for index := 0; index < 12; index++ {
		if _, err := store.SendMessage("source", strings.Repeat("x", index+1), nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := store.ReceiveMessages("source", 99, 0)
	if err != nil || len(messages) != 10 {
		t.Fatalf("receive max should clamp to 10: count=%d err=%v", len(messages), err)
	}
	if err := store.DeleteMessage("missing", "receipt"); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("missing delete queue error=%v", err)
	}
	if err := store.ChangeVisibility("missing", "receipt", 1); !errors.Is(err, ErrQueueNotFound) {
		t.Fatalf("missing visibility queue error=%v", err)
	}
}
