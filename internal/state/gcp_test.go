package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGCPStatePersistsAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGCSBucket("test-project", "assets", "asia-northeast3", "STANDARD"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutGCSObject("assets", "hello.txt", []byte("hello"), "text/plain", map[string]string{"env": "test"}); err != nil {
		t.Fatal(err)
	}
	topic := "projects/test-project/topics/events"
	subscription := "projects/test-project/subscriptions/worker"
	if _, err := store.CreatePubSubTopic(topic, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePubSubSubscription(subscription, topic, 10, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishPubSub(topic, []PubSubMessage{{Data: []byte("event")}}); err != nil {
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
	object, body, err := reopened.GCSObject("assets", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello" || object.Metadata["env"] != "test" {
		t.Fatalf("unexpected GCS object: %+v body=%q", object, body)
	}
	messages, err := reopened.PullPubSub(subscription, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || string(messages[0].Data) != "event" {
		t.Fatalf("unexpected Pub/Sub messages: %+v", messages)
	}
}

func TestFailedGCSSaveKeepsPreviousObjectGeneration(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGCSBucket("test-project", "assets", "asia-northeast3", "STANDARD"); err != nil {
		t.Fatal(err)
	}
	original, err := store.PutGCSObject("assets", "hello.txt", []byte("original"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	restoreStateFile := obstructStateFile(t, dir)
	if _, err := store.PutGCSObject("assets", "hello.txt", []byte("must-not-commit"), "text/plain", nil); err == nil {
		t.Fatal("GCS object write should fail while state.json is obstructed")
	}
	restoreStateFile()
	if _, err := store.CreateGCSBucket("test-project", "later-success", "asia-northeast3", "STANDARD"); err != nil {
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
	object, body, err := reopened.GCSObject("assets", "hello.txt")
	if err != nil || string(body) != "original" || object.File != original.File {
		t.Fatalf("failed GCS object mutation persisted later: object=%+v body=%q err=%v", object, body, err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != original.File {
		t.Fatalf("failed GCS generation was not cleaned up: %+v", entries)
	}
}

func TestGCSAndPubSubStateLifecycle(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.CreateGCSBucket("test-project", "assets", "", ""); err != nil {
		t.Fatal(err)
	}
	bucket, err := store.GCSBucket("assets")
	if err != nil || bucket.Location != "US" || bucket.StorageClass != "STANDARD" {
		t.Fatalf("unexpected bucket: bucket=%+v err=%v", bucket, err)
	}
	if buckets := store.ListGCSBuckets("test-project"); len(buckets) != 1 || buckets[0].Name != "assets" {
		t.Fatalf("unexpected bucket list: %+v", buckets)
	}
	if _, err := store.PutGCSObject("assets", "docs/a.txt", []byte("a"), "text/plain", map[string]string{"stage": "created"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutGCSObject("assets", "docs/b.txt", []byte("b"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	objects, truncated, err := store.ListGCSObjects("assets", "docs/", "", 1)
	if err != nil || !truncated || len(objects) != 1 || objects[0].Name != "docs/a.txt" {
		t.Fatalf("unexpected first object page: objects=%+v truncated=%v err=%v", objects, truncated, err)
	}
	objects, truncated, err = store.ListGCSObjects("assets", "docs/", "docs/a.txt", 10)
	if err != nil || truncated || len(objects) != 1 || objects[0].Name != "docs/b.txt" {
		t.Fatalf("unexpected second object page: objects=%+v truncated=%v err=%v", objects, truncated, err)
	}
	patched, err := store.PatchGCSObject("assets", "docs/a.txt", "application/json", map[string]string{"stage": "updated"})
	if err != nil || patched.Metageneration != 2 || patched.ContentType != "application/json" || patched.Metadata["stage"] != "updated" {
		t.Fatalf("unexpected patched object: object=%+v err=%v", patched, err)
	}
	if err := store.DeleteGCSBucket("assets"); !errors.Is(err, ErrGCSBucketNotEmpty) {
		t.Fatalf("non-empty bucket delete should fail: %v", err)
	}
	for _, name := range []string{"docs/a.txt", "docs/b.txt"} {
		if err := store.DeleteGCSObject("assets", name); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteGCSBucket("assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GCSBucket("assets"); !errors.Is(err, ErrGCSBucketNotFound) {
		t.Fatalf("deleted bucket should be missing: %v", err)
	}

	now := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	topic := "projects/test-project/topics/events"
	dlqTopic := "projects/test-project/topics/events-dlq"
	subscription := "projects/test-project/subscriptions/worker"
	for _, name := range []string{topic, dlqTopic} {
		if _, err := store.CreatePubSubTopic(name, map[string]string{"env": "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := store.PubSubTopic(topic); err != nil || got.Labels["env"] != "test" {
		t.Fatalf("unexpected topic: topic=%+v err=%v", got, err)
	}
	if topics := store.ListPubSubTopics("projects/test-project/"); len(topics) != 2 {
		t.Fatalf("unexpected topic list: %+v", topics)
	}
	if _, err := store.CreatePubSubSubscription(subscription, topic, 10, nil, true); err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdatePubSubSubscription(subscription, 20, map[string]string{"stage": "updated"}, dlqTopic, 5, true, true, true)
	if err != nil || updated.AckDeadlineSeconds != 20 || updated.Labels["stage"] != "updated" || updated.DeadLetterTopic != dlqTopic || updated.MaxDeliveryAttempts != 5 {
		t.Fatalf("unexpected subscription update: subscription=%+v err=%v", updated, err)
	}
	if subscriptions := store.ListPubSubSubscriptions("projects/test-project/"); len(subscriptions) != 1 {
		t.Fatalf("unexpected subscription list: %+v", subscriptions)
	}
	if names := store.PubSubTopicSubscriptions(topic); len(names) != 1 || names[0] != subscription {
		t.Fatalf("unexpected topic subscriptions: %+v", names)
	}
	ids, err := store.PublishPubSub(topic, []PubSubMessage{{Data: []byte("event"), Attributes: map[string]string{"env": "test"}}})
	if err != nil || len(ids) != 1 || ids[0] == "" {
		t.Fatalf("unexpected publish: ids=%+v err=%v", ids, err)
	}
	pulled, err := store.PullPubSub(subscription, 1, 30)
	if err != nil || len(pulled) != 1 || pulled[0].AckID == "" || string(pulled[0].Data) != "event" {
		t.Fatalf("unexpected pull: messages=%+v err=%v", pulled, err)
	}
	if err := store.ModifyPubSubAckDeadline(subscription, []string{pulled[0].AckID}, []int32{0}); err != nil {
		t.Fatal(err)
	}
	pulledAgain, err := store.PullPubSub(subscription, 1, 30)
	if err != nil || len(pulledAgain) != 1 || pulledAgain[0].DeliveryAttempt != 2 {
		t.Fatalf("unexpected redelivery: messages=%+v err=%v", pulledAgain, err)
	}
	if err := store.AckPubSub(subscription, []string{pulledAgain[0].AckID}); err != nil {
		t.Fatal(err)
	}
	if stored, err := store.PubSubSubscription(subscription); err != nil || len(stored.Messages) != 0 {
		t.Fatalf("acked message remained: subscription=%+v err=%v", stored, err)
	}
	if err := store.DeletePubSubSubscription(subscription); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePubSubTopic(topic); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePubSubTopic(dlqTopic); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishPubSub(topic, nil); !errors.Is(err, ErrPubSubTopicNotFound) {
		t.Fatalf("publish to deleted topic should fail: %v", err)
	}

	token := EncodePageToken("projects/test-project/topics/events")
	if decoded := DecodePageToken(token); decoded != "projects/test-project/topics/events" {
		t.Fatalf("page token round trip=%q", decoded)
	}
	if decoded := DecodePageToken("%%%"); decoded != "" {
		t.Fatalf("invalid page token=%q", decoded)
	}
}
