package state

import (
	"errors"
	"testing"
)

func TestGCSStateErrorPaginationAndReplacementEdges(t *testing.T) {
	store := openEdgeStore(t)
	if _, err := store.GCSBucket("missing"); !errors.Is(err, ErrGCSBucketNotFound) {
		t.Fatalf("missing GCS bucket lookup error=%v", err)
	}
	if err := store.DeleteGCSBucket("missing"); !errors.Is(err, ErrGCSBucketNotFound) {
		t.Fatalf("missing GCS bucket delete error=%v", err)
	}
	bucket, err := store.CreateGCSBucket("one", "assets", "", "")
	if err != nil || bucket.Location != "US" || bucket.StorageClass != "STANDARD" {
		t.Fatalf("unexpected default GCS bucket: %+v err=%v", bucket, err)
	}
	again, err := store.CreateGCSBucket("two", "assets", "EU", "NEARLINE")
	if err != nil || again.Project != "one" {
		t.Fatalf("duplicate GCS bucket should preserve original: %+v err=%v", again, err)
	}
	if got := store.ListGCSBuckets("missing"); len(got) != 0 {
		t.Fatalf("project filter should exclude buckets: %+v", got)
	}
	if _, err := store.PutGCSObject("missing", "one", nil, "", nil); !errors.Is(err, ErrGCSBucketNotFound) {
		t.Fatalf("missing GCS put bucket error=%v", err)
	}
	first, err := store.PutGCSObject("assets", "a", []byte("one"), "text/plain", map[string]string{"version": "one"})
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := store.PutGCSObject("assets", "a", []byte("two"), "text/plain", map[string]string{"version": "two"})
	if err != nil || replaced.Generation <= first.Generation || replaced.CreatedAt != first.CreatedAt {
		t.Fatalf("unexpected GCS replacement: first=%+v replaced=%+v err=%v", first, replaced, err)
	}
	if _, err := store.PutGCSObject("assets", "b", []byte("b"), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GCSObject("missing", "a"); !errors.Is(err, ErrGCSBucketNotFound) {
		t.Fatalf("missing GCS object bucket error=%v", err)
	}
	if _, _, err := store.GCSObject("assets", "missing"); !errors.Is(err, ErrGCSObjectNotFound) {
		t.Fatalf("missing GCS object error=%v", err)
	}
	if _, _, err := store.ListGCSObjects("missing", "", "", 1); !errors.Is(err, ErrGCSBucketNotFound) {
		t.Fatalf("missing GCS list bucket error=%v", err)
	}
	objects, truncated, err := store.ListGCSObjects("assets", "", "", 1)
	if err != nil || len(objects) != 1 || !truncated || objects[0].Name != "a" {
		t.Fatalf("unexpected GCS object page: %+v truncated=%v err=%v", objects, truncated, err)
	}
	objects, truncated, err = store.ListGCSObjects("assets", "", "a", 0)
	if err != nil || len(objects) != 1 || truncated || objects[0].Name != "b" {
		t.Fatalf("unexpected GCS continuation: %+v truncated=%v err=%v", objects, truncated, err)
	}
	if _, err := store.PatchGCSObject("missing", "a", "", nil); !errors.Is(err, ErrGCSBucketNotFound) {
		t.Fatalf("missing GCS patch bucket error=%v", err)
	}
	if _, err := store.PatchGCSObject("assets", "missing", "", nil); !errors.Is(err, ErrGCSObjectNotFound) {
		t.Fatalf("missing GCS patch object error=%v", err)
	}
	if err := store.DeleteGCSObject("missing", "a"); !errors.Is(err, ErrGCSBucketNotFound) {
		t.Fatalf("missing GCS delete bucket error=%v", err)
	}
	if err := store.DeleteGCSObject("assets", "missing"); !errors.Is(err, ErrGCSObjectNotFound) {
		t.Fatalf("missing GCS delete object error=%v", err)
	}
	if err := store.DeleteGCSBucket("assets"); !errors.Is(err, ErrGCSBucketNotEmpty) {
		t.Fatalf("non-empty GCS bucket delete error=%v", err)
	}
}

func TestPubSubStateErrorDeletionAndDeadlineEdges(t *testing.T) {
	store := openEdgeStore(t)
	topic := "projects/test/topics/events"
	deadLetter := "projects/test/topics/dead-letter"
	subscription := "projects/test/subscriptions/worker"

	if _, err := store.PubSubTopic("missing"); !errors.Is(err, ErrPubSubTopicNotFound) {
		t.Fatalf("missing topic lookup error=%v", err)
	}
	if err := store.DeletePubSubTopic("missing"); !errors.Is(err, ErrPubSubTopicNotFound) {
		t.Fatalf("missing topic delete error=%v", err)
	}
	created, err := store.CreatePubSubTopic(topic, map[string]string{"env": "test"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.CreatePubSubTopic(topic, map[string]string{"env": "changed"})
	if err != nil || duplicate.Labels["env"] != created.Labels["env"] {
		t.Fatalf("duplicate topic should preserve labels: %+v err=%v", duplicate, err)
	}
	if _, err := store.CreatePubSubSubscription(subscription, "missing", 0, nil, false); !errors.Is(err, ErrPubSubTopicNotFound) {
		t.Fatalf("missing subscription topic error=%v", err)
	}
	sub, err := store.CreatePubSubSubscription(subscription, topic, 0, map[string]string{"env": "test"}, true)
	if err != nil || sub.AckDeadlineSeconds != 10 {
		t.Fatalf("unexpected default subscription: %+v err=%v", sub, err)
	}
	duplicateSub, err := store.CreatePubSubSubscription(subscription, topic, 30, nil, false)
	if err != nil || duplicateSub.AckDeadlineSeconds != 10 {
		t.Fatalf("duplicate subscription should preserve original: %+v err=%v", duplicateSub, err)
	}
	if _, err := store.PubSubSubscription("missing"); !errors.Is(err, ErrPubSubSubscriptionNotFound) {
		t.Fatalf("missing subscription lookup error=%v", err)
	}
	if _, err := store.UpdatePubSubSubscription("missing", 10, nil, "", 0, true, false, false); !errors.Is(err, ErrPubSubSubscriptionNotFound) {
		t.Fatalf("missing subscription update error=%v", err)
	}
	if _, err := store.UpdatePubSubSubscription(subscription, 10, nil, "missing", 5, false, false, true); !errors.Is(err, ErrPubSubTopicNotFound) {
		t.Fatalf("missing dead-letter topic error=%v", err)
	}
	if _, err := store.PublishPubSub("missing", nil); !errors.Is(err, ErrPubSubTopicNotFound) {
		t.Fatalf("missing publish topic error=%v", err)
	}
	if _, err := store.PullPubSub("missing", 1, 1); !errors.Is(err, ErrPubSubSubscriptionNotFound) {
		t.Fatalf("missing pull subscription error=%v", err)
	}
	if err := store.AckPubSub("missing", nil); !errors.Is(err, ErrPubSubSubscriptionNotFound) {
		t.Fatalf("missing ack subscription error=%v", err)
	}
	if err := store.ModifyPubSubAckDeadline("missing", nil, nil); !errors.Is(err, ErrPubSubSubscriptionNotFound) {
		t.Fatalf("missing deadline subscription error=%v", err)
	}
	if err := store.PurgePubSubSubscription("missing"); !errors.Is(err, ErrPubSubSubscriptionNotFound) {
		t.Fatalf("missing purge subscription error=%v", err)
	}
	if err := store.DeletePubSubSubscription("missing"); !errors.Is(err, ErrPubSubSubscriptionNotFound) {
		t.Fatalf("missing subscription delete error=%v", err)
	}

	if _, err := store.CreatePubSubTopic(deadLetter, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdatePubSubSubscription(subscription, 15, map[string]string{"updated": "true"}, deadLetter, 5, true, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishPubSub(topic, []PubSubMessage{{Data: []byte("body")}}); err != nil {
		t.Fatal(err)
	}
	messages, err := store.PullPubSub(subscription, 0, 0)
	if err != nil || len(messages) != 1 || messages[0].AckID == "" {
		t.Fatalf("unexpected default pull: %+v err=%v", messages, err)
	}
	if err := store.ModifyPubSubAckDeadline(subscription, []string{messages[0].AckID}, []int32{0}); err != nil {
		t.Fatal(err)
	}
	if err := store.AckPubSub(subscription, []string{"not-the-message"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePubSubTopic(topic); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PullPubSub(subscription, 1, 1); !errors.Is(err, ErrPubSubTopicNotFound) {
		t.Fatalf("deleted source topic pull error=%v", err)
	}

	if token := DecodePageToken("invalid!"); token != "" {
		t.Fatalf("invalid page token decoded unexpectedly: %q", token)
	}
	value := "projects/test/topics/a"
	if decoded := DecodePageToken(EncodePageToken(value)); decoded != value {
		t.Fatalf("page token round trip=%q", decoded)
	}
}
