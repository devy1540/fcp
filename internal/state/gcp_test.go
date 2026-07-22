package state

import "testing"

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

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
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
