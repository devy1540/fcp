package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResetWorkloadDataPreservesInfrastructureAndKeys(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("aws-assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("aws-assets", "hello.txt", []byte("aws"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	upload, err := store.CreateMultipartUpload("aws-assets", "pending.bin", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	pendingPart, err := store.UploadMultipartPart("aws-assets", "pending.bin", upload.ID, 1, []byte("pending"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateQueue("jobs", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SendMessage("jobs", "work", nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateQueue("fifo-jobs.fifo", map[string]string{"FifoQueue": "true"}); err != nil {
		t.Fatal(err)
	}
	beforeResetFIFO, err := store.SendMessageWithOptions("fifo-jobs.fifo", "fifo-work", nil, 0, SendMessageOptions{MessageGroupID: "test", MessageDeduplicationID: "repeatable"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGCSBucket("test-project", "gcp-assets", "asia-northeast3", "STANDARD"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutGCSObject("gcp-assets", "hello.txt", []byte("gcp"), "text/plain", nil); err != nil {
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
	documentName := "projects/test-project/databases/(default)/documents/jobs/1"
	if err := store.MutateFirestore(func(documents map[string]*FirestoreDocument, now time.Time) error {
		documents[documentName] = &FirestoreDocument{Name: documentName, Proto: []byte("document"), CreateTime: now, UpdateTime: now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	secretName := "projects/test-project/secrets/config"
	if _, err := store.CreateSecret(secretName, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSecretVersion(secretName, []byte("preserved-secret")); err != nil {
		t.Fatal(err)
	}
	keyRing := "projects/test-project/locations/global/keyRings/local"
	if _, err := store.CreateKMSKeyRing(keyRing); err != nil {
		t.Fatal(err)
	}
	keyName := keyRing + "/cryptoKeys/config"
	if _, err := store.CreateKMSCryptoKey(KMSCryptoKey{
		Name: keyName, Purpose: "ENCRYPT_DECRYPT", Algorithm: "GOOGLE_SYMMETRIC_ENCRYPTION", PrimaryVersion: 1, CreateTime: time.Now().UTC(),
		Versions: []KMSKeyVersion{{Number: 1, Algorithm: "GOOGLE_SYMMETRIC_ENCRYPTION", State: "ENABLED", KeyMaterial: []byte("preserved-key"), CreateTime: time.Now().UTC()}},
	}); err != nil {
		t.Fatal(err)
	}
	accountName := "projects/-/serviceAccounts/local@test-project.iam.gserviceaccount.com"
	if _, err := store.IAMServiceAccount(accountName, func() ([]byte, error) { return []byte("preserved-private-key"), nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordFCMMessage("test-project", json.RawMessage(`{"token":"local"}`), false); err != nil {
		t.Fatal(err)
	}

	if err := store.ResetWorkloadData(); err != nil {
		t.Fatal(err)
	}

	assertWorkloadIsEmpty(t, store, subscription)
	if _, err := os.Stat(filepath.Join(dir, "objects", pendingPart.File)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("multipart part file survived workload reset: %v", err)
	}
	if len(store.ListBuckets()) != 1 || len(store.ListQueues("")) != 2 || len(store.ListGCSBuckets("")) != 1 {
		t.Fatal("reset removed bucket or queue infrastructure")
	}
	afterResetFIFO, err := store.SendMessageWithOptions("fifo-jobs.fifo", "fifo-work", nil, 0, SendMessageOptions{MessageGroupID: "test", MessageDeduplicationID: "repeatable"})
	if err != nil {
		t.Fatal(err)
	}
	if afterResetFIFO.MessageID == beforeResetFIFO.MessageID {
		t.Fatal("reset preserved FIFO deduplication history")
	}
	if len(store.ListPubSubTopics("")) != 1 || len(store.ListPubSubSubscriptions("")) != 1 {
		t.Fatal("reset removed Pub/Sub infrastructure")
	}
	secretVersion, err := store.SecretVersion(secretName, 1)
	if err != nil || string(secretVersion.Payload) != "preserved-secret" {
		t.Fatalf("secret was not preserved: version=%+v err=%v", secretVersion, err)
	}
	keyVersion, err := store.KMSKeyVersion(keyName, 1)
	if err != nil || string(keyVersion.KeyMaterial) != "preserved-key" {
		t.Fatalf("KMS key was not preserved: version=%+v err=%v", keyVersion, err)
	}
	account, err := store.ExistingIAMServiceAccount(accountName)
	if err != nil || string(account.PrivateKey) != "preserved-private-key" {
		t.Fatalf("IAM key was not preserved: account=%+v err=%v", account, err)
	}
}

func TestPurgePubSubSubscriptionPreservesSubscription(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
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
	if err := store.PurgePubSubSubscription(subscription); err != nil {
		t.Fatal(err)
	}
	stored, err := store.PubSubSubscription(subscription)
	if err != nil || len(stored.Messages) != 0 {
		t.Fatalf("subscription purge failed: subscription=%+v err=%v", stored, err)
	}
}

func assertWorkloadIsEmpty(t *testing.T, store *Store, subscription string) {
	t.Helper()
	objects, _, err := store.ListObjects("aws-assets", "", "", 0)
	if err != nil || len(objects) != 0 {
		t.Fatalf("AWS objects remain: objects=%+v err=%v", objects, err)
	}
	uploads, err := store.ListMultipartUploads("aws-assets", "")
	if err != nil || len(uploads) != 0 {
		t.Fatalf("AWS multipart uploads remain: uploads=%+v err=%v", uploads, err)
	}
	queue, err := store.Queue("jobs")
	if err != nil || len(queue.Messages) != 0 {
		t.Fatalf("SQS messages remain: queue=%+v err=%v", queue, err)
	}
	fifoQueue, err := store.Queue("fifo-jobs.fifo")
	if err != nil || len(fifoQueue.Messages) != 0 || len(fifoQueue.Deduplication) != 0 {
		t.Fatalf("FIFO workload remains: queue=%+v err=%v", fifoQueue, err)
	}
	gcsObjects, _, err := store.ListGCSObjects("gcp-assets", "", "", 0)
	if err != nil || len(gcsObjects) != 0 {
		t.Fatalf("GCS objects remain: objects=%+v err=%v", gcsObjects, err)
	}
	pubsubSubscription, err := store.PubSubSubscription(subscription)
	if err != nil || len(pubsubSubscription.Messages) != 0 {
		t.Fatalf("Pub/Sub messages remain: subscription=%+v err=%v", pubsubSubscription, err)
	}
	if len(store.ListFirestoreDocuments("")) != 0 || len(store.ListFCMMessages("")) != 0 {
		t.Fatal("Firestore documents or FCM captures remain")
	}
}
