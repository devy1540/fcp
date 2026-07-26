package server

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	iamcredentials "cloud.google.com/go/iam/credentials/apiv1"
	"cloud.google.com/go/iam/credentials/apiv1/credentialspb"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"cloud.google.com/go/storage"
	"github.com/devy1540/fcp/internal/state"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func TestGCSWithOfficialGoClient(t *testing.T) {
	server := newTestServer(t)
	t.Setenv("STORAGE_EMULATOR_HOST", server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := storage.NewClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	bucket := client.Bucket("gcp-assets")
	if err := bucket.Create(ctx, "test-project", &storage.BucketAttrs{Location: "asia-northeast3"}); err != nil {
		t.Fatal(err)
	}
	attrs, err := bucket.Attrs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if attrs.Name != "gcp-assets" || attrs.Location != "ASIA-NORTHEAST3" {
		t.Fatalf("unexpected bucket attrs: %+v", attrs)
	}

	object := bucket.Object("docs/hello.txt")
	writer := object.NewWriter(ctx)
	writer.ChunkSize = 0
	writer.ContentType = "text/plain"
	writer.Metadata = map[string]string{"env": "test"}
	if _, err := writer.Write([]byte("hello gcp")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	objectAttrs, err := object.Attrs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if objectAttrs.Size != 9 || objectAttrs.Metadata["env"] != "test" {
		t.Fatalf("unexpected object attrs: %+v", objectAttrs)
	}
	reader, err := object.NewRangeReader(ctx, 6, 3)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if string(body) != "gcp" {
		t.Fatalf("unexpected range body: %q", body)
	}
	updated, err := object.Update(ctx, storage.ObjectAttrsToUpdate{Metadata: map[string]string{"env": "updated"}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Metadata["env"] != "updated" {
		t.Fatalf("metadata was not updated: %+v", updated.Metadata)
	}

	resumable := bucket.Object("docs/resumable.txt")
	resumableWriter := resumable.NewWriter(ctx)
	resumableWriter.ChunkSize = 256 * 1024
	resumablePayload := bytes.Repeat([]byte("fcp-gcp-"), 40_000)
	if _, err := resumableWriter.Write(resumablePayload); err != nil {
		t.Fatal(err)
	}
	if err := resumableWriter.Close(); err != nil {
		t.Fatal(err)
	}
	resumableReader, err := resumable.NewReader(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resumableBody, err := io.ReadAll(resumableReader)
	if err != nil {
		t.Fatal(err)
	}
	_ = resumableReader.Close()
	if !bytes.Equal(resumableBody, resumablePayload) {
		t.Fatalf("unexpected resumable body size: %d", len(resumableBody))
	}

	objectIterator := bucket.Objects(ctx, &storage.Query{Prefix: "docs/"})
	listed, err := objectIterator.Next()
	if err != nil {
		t.Fatal(err)
	}
	if listed.Name != "docs/hello.txt" {
		t.Fatalf("unexpected listed object: %+v", listed)
	}
	listed, err = objectIterator.Next()
	if err != nil || listed.Name != "docs/resumable.txt" {
		t.Fatalf("unexpected second listed object: %+v err=%v", listed, err)
	}
	if _, err := objectIterator.Next(); !errors.Is(err, iterator.Done) {
		t.Fatalf("expected iterator.Done, got %v", err)
	}
	if err := object.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := resumable.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := bucket.Delete(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPubSubWithOfficialGoClient(t *testing.T) {
	listener := newGCPTestServer(t)
	t.Setenv("PUBSUB_EMULATOR_HOST", listener.Addr().String())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := pubsub.NewClient(ctx, "test-project")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	topicName := "projects/test-project/topics/events"
	subscriptionName := "projects/test-project/subscriptions/worker"
	if _, err := client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topicName}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{Name: subscriptionName, Topic: topicName, AckDeadlineSeconds: 10}); err != nil {
		t.Fatal(err)
	}

	publisher := client.Publisher("events")
	publisher.PublishSettings.CountThreshold = 1
	messageID, err := publisher.Publish(ctx, &pubsub.Message{Data: []byte("hello pubsub"), Attributes: map[string]string{"env": "test"}}).Get(ctx)
	publisher.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if messageID == "" {
		t.Fatal("empty message ID")
	}

	receiveCtx, stopReceive := context.WithCancel(ctx)
	received := make(chan *pubsub.Message, 1)
	subscriber := client.Subscriber("worker")
	subscriber.ReceiveSettings.NumGoroutines = 1
	subscriber.ReceiveSettings.MaxOutstandingMessages = 1
	err = subscriber.Receive(receiveCtx, func(_ context.Context, message *pubsub.Message) {
		message.Ack()
		received <- message
		stopReceive()
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case message := <-received:
		if string(message.Data) != "hello pubsub" || message.Attributes["env"] != "test" || message.ID != messageID {
			t.Fatalf("unexpected message: %+v", message)
		}
	default:
		t.Fatal("no message received")
	}

	topic, err := client.TopicAdminClient.GetTopic(ctx, &pubsubpb.GetTopicRequest{Topic: topicName})
	if err != nil || topic.GetName() != topicName {
		t.Fatalf("unexpected topic: topic=%+v err=%v", topic, err)
	}
	topicIterator := client.TopicAdminClient.ListTopics(ctx, &pubsubpb.ListTopicsRequest{Project: "projects/test-project"})
	listedTopic, err := topicIterator.Next()
	if err != nil || listedTopic.GetName() != topicName {
		t.Fatalf("unexpected topic list: topic=%+v err=%v", listedTopic, err)
	}
	if _, err := topicIterator.Next(); !errors.Is(err, iterator.Done) {
		t.Fatalf("topic iterator should finish: %v", err)
	}
	subscription, err := client.SubscriptionAdminClient.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{Subscription: subscriptionName})
	if err != nil || subscription.GetTopic() != topicName {
		t.Fatalf("unexpected subscription: subscription=%+v err=%v", subscription, err)
	}
	subscriptionIterator := client.SubscriptionAdminClient.ListSubscriptions(ctx, &pubsubpb.ListSubscriptionsRequest{Project: "projects/test-project"})
	listedSubscription, err := subscriptionIterator.Next()
	if err != nil || listedSubscription.GetName() != subscriptionName {
		t.Fatalf("unexpected subscription list: subscription=%+v err=%v", listedSubscription, err)
	}
	if _, err := subscriptionIterator.Next(); !errors.Is(err, iterator.Done) {
		t.Fatalf("subscription iterator should finish: %v", err)
	}
	topicSubscriptions := client.TopicAdminClient.ListTopicSubscriptions(ctx, &pubsubpb.ListTopicSubscriptionsRequest{Topic: topicName})
	listedSubscriptionName, err := topicSubscriptions.Next()
	if err != nil || listedSubscriptionName != subscriptionName {
		t.Fatalf("unexpected topic subscription list: name=%q err=%v", listedSubscriptionName, err)
	}
	if _, err := topicSubscriptions.Next(); !errors.Is(err, iterator.Done) {
		t.Fatalf("topic subscription iterator should finish: %v", err)
	}
	if err := client.SubscriptionAdminClient.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: subscriptionName}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SubscriptionAdminClient.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{Subscription: subscriptionName}); status.Code(err) != codes.NotFound {
		t.Fatalf("deleted subscription should be missing: %v", err)
	}
	if err := client.TopicAdminClient.DeleteTopic(ctx, &pubsubpb.DeleteTopicRequest{Topic: topicName}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.TopicAdminClient.GetTopic(ctx, &pubsubpb.GetTopicRequest{Topic: topicName}); status.Code(err) != codes.NotFound {
		t.Fatalf("deleted topic should be missing: %v", err)
	}
}

func TestPubSubDeadLetterPolicyWithOfficialGoClient(t *testing.T) {
	listener := newGCPTestServer(t)
	t.Setenv("PUBSUB_EMULATOR_HOST", listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := pubsub.NewClient(ctx, "test-project")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	topic := "projects/test-project/topics/jobs"
	dlqTopic := "projects/test-project/topics/jobs-dlq"
	subscription := "projects/test-project/subscriptions/jobs-worker"
	dlqSubscription := "projects/test-project/subscriptions/jobs-dlq-worker"
	for _, name := range []string{topic, dlqTopic} {
		if _, err := client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	for name, source := range map[string]string{subscription: topic, dlqSubscription: dlqTopic} {
		if _, err := client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{Name: name, Topic: source, AckDeadlineSeconds: 10}); err != nil {
			t.Fatal(err)
		}
	}
	updated, err := client.SubscriptionAdminClient.UpdateSubscription(ctx, &pubsubpb.UpdateSubscriptionRequest{
		Subscription: &pubsubpb.Subscription{Name: subscription, DeadLetterPolicy: &pubsubpb.DeadLetterPolicy{DeadLetterTopic: dlqTopic, MaxDeliveryAttempts: 5}},
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"dead_letter_policy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetDeadLetterPolicy().GetDeadLetterTopic() != dlqTopic || updated.GetDeadLetterPolicy().GetMaxDeliveryAttempts() != 5 {
		t.Fatalf("unexpected dead letter policy: %+v", updated.GetDeadLetterPolicy())
	}

	publisher := client.Publisher("jobs")
	publisher.PublishSettings.CountThreshold = 1
	if _, err := publisher.Publish(ctx, &pubsub.Message{Data: []byte("failed job")}).Get(ctx); err != nil {
		t.Fatal(err)
	}
	publisher.Stop()
	for attempt := int32(1); attempt <= 5; attempt++ {
		pulled, err := client.SubscriptionAdminClient.Pull(ctx, &pubsubpb.PullRequest{Subscription: subscription, MaxMessages: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(pulled.GetReceivedMessages()) != 1 || pulled.GetReceivedMessages()[0].GetDeliveryAttempt() != attempt {
			t.Fatalf("attempt %d: unexpected pull response %+v", attempt, pulled)
		}
		if err := client.SubscriptionAdminClient.ModifyAckDeadline(ctx, &pubsubpb.ModifyAckDeadlineRequest{
			Subscription: subscription, AckIds: []string{pulled.GetReceivedMessages()[0].GetAckId()}, AckDeadlineSeconds: 0,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mainPull, err := client.SubscriptionAdminClient.Pull(ctx, &pubsubpb.PullRequest{Subscription: subscription, MaxMessages: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(mainPull.GetReceivedMessages()) != 0 {
		t.Fatalf("message must leave the source subscription after five attempts: %+v", mainPull)
	}
	dlqPull, err := client.SubscriptionAdminClient.Pull(ctx, &pubsubpb.PullRequest{Subscription: dlqSubscription, MaxMessages: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(dlqPull.GetReceivedMessages()) != 1 || string(dlqPull.GetReceivedMessages()[0].GetMessage().GetData()) != "failed job" {
		t.Fatalf("unexpected DLQ message: %+v", dlqPull)
	}
}

func TestFirestoreWithOfficialGoClient(t *testing.T) {
	listener := newGCPTestServer(t)
	t.Setenv("FIRESTORE_EMULATOR_HOST", listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := firestore.NewClient(ctx, "test-project")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	collection := client.Collection("notifications")
	doc := collection.Doc("APP#one#CHECK")
	if _, err := doc.Set(ctx, map[string]any{
		"pk": "APP#one", "sk": "CHECK", "count": int64(1), "obsolete": "remove-me",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Set(ctx, map[string]any{"enabled": true}, firestore.MergeAll); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Update(ctx, []firestore.Update{
		{Path: "count", Value: firestore.Increment(2)},
		{Path: "obsolete", Value: firestore.Delete},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := doc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := snapshot.DataAt("count"); err != nil || count != int64(3) {
		t.Fatalf("unexpected increment result: value=%v err=%v", count, err)
	}
	if _, err := snapshot.DataAt("obsolete"); err == nil {
		t.Fatal("deleted field is still present")
	}

	batch := client.Batch()
	batch.Set(collection.Doc("APP#one#A"), map[string]any{"pk": "APP#one", "sk": "A"})
	batch.Set(collection.Doc("APP#one#B"), map[string]any{"pk": "APP#one", "sk": "B"})
	if _, err := batch.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	iterator := collection.Where("pk", "==", "APP#one").Where("sk", ">=", "A").Where("sk", "<=", "B").Documents(ctx)
	queried, err := iterator.GetAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(queried) != 2 {
		t.Fatalf("unexpected filtered query count: %d", len(queried))
	}

	err = client.RunTransaction(ctx, func(ctx context.Context, transaction *firestore.Transaction) error {
		current, err := transaction.Get(doc)
		if err != nil {
			return err
		}
		enabled, err := current.DataAt("enabled")
		if err != nil || enabled != true {
			return errors.New("transaction read mismatch")
		}
		return transaction.Update(doc, []firestore.Update{{Path: "status", Value: "READY"}})
	})
	if err != nil {
		t.Fatal(err)
	}

	ordered := collection.OrderBy(firestore.DocumentID, firestore.Asc).Limit(2).Documents(ctx)
	firstPage, err := ordered.GetAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 2 {
		t.Fatalf("unexpected first page size: %d", len(firstPage))
	}
	secondPage, err := collection.OrderBy(firestore.DocumentID, firestore.Asc).StartAfter(firstPage[1]).Documents(ctx).GetAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage) != 1 {
		t.Fatalf("unexpected second page size: %d", len(secondPage))
	}
}

func TestFirestoreGeneratedClientCRUDAndBatchWrite(t *testing.T) {
	listener := newGCPTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	client := firestorepb.NewFirestoreClient(connection)

	database := "projects/test-project/databases/(default)"
	parent := database + "/documents"
	value := &firestorepb.Value{ValueType: &firestorepb.Value_StringValue{StringValue: "created"}}
	created, err := client.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
		Parent: parent, CollectionId: "direct", DocumentId: "one",
		Document: &firestorepb.Document{Fields: map[string]*firestorepb.Value{"status": value}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetName() != parent+"/direct/one" {
		t.Fatalf("unexpected document name: %s", created.GetName())
	}
	if _, err := client.CreateDocument(ctx, &firestorepb.CreateDocumentRequest{
		Parent: parent, CollectionId: "direct", DocumentId: "one", Document: &firestorepb.Document{},
	}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate create should fail: %v", err)
	}
	got, err := client.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: created.GetName()})
	if err != nil || got.GetFields()["status"].GetStringValue() != "created" {
		t.Fatalf("unexpected get document: document=%+v err=%v", got, err)
	}
	listed, err := client.ListDocuments(ctx, &firestorepb.ListDocumentsRequest{
		Parent: parent, CollectionId: "direct", PageSize: 1,
	})
	if err != nil || len(listed.GetDocuments()) != 1 || listed.GetDocuments()[0].GetName() != created.GetName() {
		t.Fatalf("unexpected list documents: response=%+v err=%v", listed, err)
	}
	updatedValue := &firestorepb.Value{ValueType: &firestorepb.Value_StringValue{StringValue: "updated"}}
	updated, err := client.UpdateDocument(ctx, &firestorepb.UpdateDocumentRequest{
		Document:   &firestorepb.Document{Name: created.GetName(), Fields: map[string]*firestorepb.Value{"status": updatedValue}},
		UpdateMask: &firestorepb.DocumentMask{FieldPaths: []string{"status"}},
	})
	if err != nil || updated.GetFields()["status"].GetStringValue() != "updated" {
		t.Fatalf("unexpected update document: document=%+v err=%v", updated, err)
	}
	batchValue := &firestorepb.Value{ValueType: &firestorepb.Value_StringValue{StringValue: "batch"}}
	batch, err := client.BatchWrite(ctx, &firestorepb.BatchWriteRequest{
		Database: database,
		Writes: []*firestorepb.Write{{
			Operation: &firestorepb.Write_Update{Update: &firestorepb.Document{
				Name: parent + "/direct/two", Fields: map[string]*firestorepb.Value{"status": batchValue},
			}},
		}},
	})
	if err != nil || len(batch.GetStatus()) != 1 || batch.GetStatus()[0].GetCode() != int32(codes.OK) {
		t.Fatalf("unexpected batch write: response=%+v err=%v", batch, err)
	}
	transaction, err := client.BeginTransaction(ctx, &firestorepb.BeginTransactionRequest{Database: database})
	if err != nil || len(transaction.GetTransaction()) == 0 {
		t.Fatalf("unexpected transaction: response=%+v err=%v", transaction, err)
	}
	if _, err := client.Rollback(ctx, &firestorepb.RollbackRequest{Database: database, Transaction: transaction.GetTransaction()}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DeleteDocument(ctx, &firestorepb.DeleteDocumentRequest{Name: created.GetName()}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetDocument(ctx, &firestorepb.GetDocumentRequest{Name: created.GetName()}); status.Code(err) != codes.NotFound {
		t.Fatalf("deleted document should be missing: %v", err)
	}
}

func TestSecretManagerWithOfficialGoClient(t *testing.T) {
	listener := newGCPTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := secretmanager.NewClient(ctx,
		option.WithEndpoint(listener.Addr().String()),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	parent := "projects/test-project"
	secret, err := client.CreateSecret(ctx, &secretmanagerpb.CreateSecretRequest{
		Parent: parent, SecretId: "notifications",
		Secret: &secretmanagerpb.Secret{Replication: &secretmanagerpb.Replication{Replication: &secretmanagerpb.Replication_Automatic_{Automatic: &secretmanagerpb.Replication_Automatic{}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	version, err := client.AddSecretVersion(ctx, &secretmanagerpb.AddSecretVersionRequest{
		Parent: secret.GetName(), Payload: &secretmanagerpb.SecretPayload{Data: []byte(`{"key":"value"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(version.GetName(), "/versions/1") {
		t.Fatalf("unexpected secret version name: %s", version.GetName())
	}
	accessed, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{Name: secret.GetName() + "/versions/latest"})
	if err != nil {
		t.Fatal(err)
	}
	if string(accessed.GetPayload().GetData()) != `{"key":"value"}` || accessed.GetPayload().DataCrc32C == nil {
		t.Fatalf("unexpected secret payload: %+v", accessed.GetPayload())
	}
	if _, err := client.DisableSecretVersion(ctx, &secretmanagerpb.DisableSecretVersionRequest{Name: version.GetName()}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{Name: version.GetName()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected disabled version failure, got %v", err)
	}
	if _, err := client.EnableSecretVersion(ctx, &secretmanagerpb.EnableSecretVersionRequest{Name: version.GetName()}); err != nil {
		t.Fatal(err)
	}
	gotSecret, err := client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: secret.GetName()})
	if err != nil || gotSecret.GetName() != secret.GetName() {
		t.Fatalf("unexpected secret: secret=%+v err=%v", gotSecret, err)
	}
	secretIterator := client.ListSecrets(ctx, &secretmanagerpb.ListSecretsRequest{Parent: parent, PageSize: 1})
	listedSecret, err := secretIterator.Next()
	if err != nil || listedSecret.GetName() != secret.GetName() {
		t.Fatalf("unexpected secret list: secret=%+v err=%v", listedSecret, err)
	}
	if _, err := secretIterator.Next(); !errors.Is(err, iterator.Done) {
		t.Fatalf("secret iterator should finish: %v", err)
	}
	updatedSecret, err := client.UpdateSecret(ctx, &secretmanagerpb.UpdateSecretRequest{
		Secret:     &secretmanagerpb.Secret{Name: secret.GetName(), Labels: map[string]string{"env": "test"}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"labels"}},
	})
	if err != nil || updatedSecret.GetLabels()["env"] != "test" {
		t.Fatalf("secret labels were not updated: secret=%+v err=%v", updatedSecret, err)
	}
	gotVersion, err := client.GetSecretVersion(ctx, &secretmanagerpb.GetSecretVersionRequest{Name: version.GetName()})
	if err != nil || gotVersion.GetState() != secretmanagerpb.SecretVersion_ENABLED {
		t.Fatalf("unexpected secret version: version=%+v err=%v", gotVersion, err)
	}
	versionIterator := client.ListSecretVersions(ctx, &secretmanagerpb.ListSecretVersionsRequest{Parent: secret.GetName(), PageSize: 1})
	listedVersion, err := versionIterator.Next()
	if err != nil || listedVersion.GetName() != version.GetName() {
		t.Fatalf("unexpected secret version list: version=%+v err=%v", listedVersion, err)
	}
	if _, err := versionIterator.Next(); !errors.Is(err, iterator.Done) {
		t.Fatalf("secret version iterator should finish: %v", err)
	}
	destroyed, err := client.DestroySecretVersion(ctx, &secretmanagerpb.DestroySecretVersionRequest{Name: version.GetName()})
	if err != nil || destroyed.GetState() != secretmanagerpb.SecretVersion_DESTROYED {
		t.Fatalf("unexpected destroyed secret version: version=%+v err=%v", destroyed, err)
	}
	if err := client.DeleteSecret(ctx, &secretmanagerpb.DeleteSecretRequest{Name: secret.GetName()}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: secret.GetName()}); status.Code(err) != codes.NotFound {
		t.Fatalf("deleted secret should be missing: %v", err)
	}
}

func TestKMSWithOfficialGoClient(t *testing.T) {
	listener := newGCPTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := kms.NewKeyManagementClient(ctx,
		option.WithEndpoint(listener.Addr().String()),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	location := "projects/test-project/locations/asia-northeast3"
	keyRing, err := client.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{Parent: location, KeyRingId: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	symmetric, err := client.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent: keyRing.GetName(), CryptoKeyId: "data-encryption",
		CryptoKey: &kmspb.CryptoKey{Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT, VersionTemplate: &kmspb.CryptoKeyVersionTemplate{Algorithm: kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION}},
	})
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := client.Encrypt(ctx, &kmspb.EncryptRequest{Name: symmetric.GetName(), Plaintext: []byte("local-dek")})
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := client.Decrypt(ctx, &kmspb.DecryptRequest{Name: symmetric.GetName(), Ciphertext: encrypted.GetCiphertext()})
	if err != nil {
		t.Fatal(err)
	}
	if string(decrypted.GetPlaintext()) != "local-dek" || decrypted.GetPlaintextCrc32C() == nil {
		t.Fatalf("unexpected decrypt response: %+v", decrypted)
	}
	gotKeyRing, err := client.GetKeyRing(ctx, &kmspb.GetKeyRingRequest{Name: keyRing.GetName()})
	if err != nil || gotKeyRing.GetName() != keyRing.GetName() {
		t.Fatalf("unexpected key ring: keyRing=%+v err=%v", gotKeyRing, err)
	}
	keyRingIterator := client.ListKeyRings(ctx, &kmspb.ListKeyRingsRequest{Parent: location})
	listedKeyRing, err := keyRingIterator.Next()
	if err != nil || listedKeyRing.GetName() != keyRing.GetName() {
		t.Fatalf("unexpected key ring list: keyRing=%+v err=%v", listedKeyRing, err)
	}
	if _, err := keyRingIterator.Next(); !errors.Is(err, iterator.Done) {
		t.Fatalf("key ring iterator should finish: %v", err)
	}
	gotSymmetric, err := client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: symmetric.GetName()})
	if err != nil || gotSymmetric.GetName() != symmetric.GetName() {
		t.Fatalf("unexpected crypto key: key=%+v err=%v", gotSymmetric, err)
	}
	keyIterator := client.ListCryptoKeys(ctx, &kmspb.ListCryptoKeysRequest{Parent: keyRing.GetName()})
	listedKey, err := keyIterator.Next()
	if err != nil || listedKey.GetName() != symmetric.GetName() {
		t.Fatalf("unexpected crypto key list: key=%+v err=%v", listedKey, err)
	}
	addedVersion, err := client.CreateCryptoKeyVersion(ctx, &kmspb.CreateCryptoKeyVersionRequest{
		Parent: symmetric.GetName(), CryptoKeyVersion: &kmspb.CryptoKeyVersion{},
	})
	if err != nil {
		t.Fatal(err)
	}
	gotVersion, err := client.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{Name: addedVersion.GetName()})
	if err != nil || gotVersion.GetName() != addedVersion.GetName() {
		t.Fatalf("unexpected crypto key version: version=%+v err=%v", gotVersion, err)
	}
	versionIterator := client.ListCryptoKeyVersions(ctx, &kmspb.ListCryptoKeyVersionsRequest{Parent: symmetric.GetName()})
	versionCount := 0
	for {
		if _, err := versionIterator.Next(); errors.Is(err, iterator.Done) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		versionCount++
	}
	if versionCount != 2 {
		t.Fatalf("unexpected crypto key version count: %d", versionCount)
	}

	signing, err := client.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
		Parent: keyRing.GetName(), CryptoKeyId: "jwt-signing",
		CryptoKey: &kmspb.CryptoKey{Purpose: kmspb.CryptoKey_ASYMMETRIC_SIGN, VersionTemplate: &kmspb.CryptoKeyVersionTemplate{Algorithm: kmspb.CryptoKeyVersion_RSA_SIGN_PKCS1_3072_SHA256}},
	})
	if err != nil {
		t.Fatal(err)
	}
	versionName := signing.GetPrimary().GetName()
	digest := sha256.Sum256([]byte("header.payload"))
	signed, err := client.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{Name: versionName, Digest: &kmspb.Digest{Digest: &kmspb.Digest_Sha256{Sha256: digest[:]}}})
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := client.GetPublicKey(ctx, &kmspb.GetPublicKeyRequest{Name: versionName})
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(publicKey.GetPem()))
	if block == nil {
		t.Fatal("public key is not PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(parsed.(*rsa.PublicKey), crypto.SHA256, digest[:], signed.GetSignature()); err != nil {
		t.Fatalf("signature verification failed: %v", err)
	}
}

func TestIAMCredentialsWithOfficialGoClient(t *testing.T) {
	listener := newGCPTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := iamcredentials.NewIamCredentialsClient(ctx,
		option.WithEndpoint(listener.Addr().String()),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	name := "projects/-/serviceAccounts/fcp@test-project.iam.gserviceaccount.com"
	signed, err := client.SignBlob(ctx, &credentialspb.SignBlobRequest{Name: name, Payload: []byte("signed-url-canonical-request")})
	if err != nil {
		t.Fatal(err)
	}
	if signed.GetKeyId() == "" || len(signed.GetSignedBlob()) == 0 {
		t.Fatalf("unexpected signBlob response: %+v", signed)
	}
	token, err := client.GenerateAccessToken(ctx, &credentialspb.GenerateAccessTokenRequest{Name: name, Scope: []string{"https://www.googleapis.com/auth/cloud-platform"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(token.GetAccessToken(), ".") != 2 || token.GetExpireTime() == nil {
		t.Fatalf("unexpected access token response: %+v", token)
	}
	idToken, err := client.GenerateIdToken(ctx, &credentialspb.GenerateIdTokenRequest{Name: name, Audience: "https://fcp.local", IncludeEmail: true})
	if err != nil || strings.Count(idToken.GetToken(), ".") != 2 {
		t.Fatalf("unexpected ID token: token=%+v err=%v", idToken, err)
	}
	signedJWT, err := client.SignJwt(ctx, &credentialspb.SignJwtRequest{Name: name, Payload: `{"sub":"fcp-test"}`})
	if err != nil || signedJWT.GetKeyId() == "" || strings.Count(signedJWT.GetSignedJwt(), ".") != 2 {
		t.Fatalf("unexpected signed JWT: response=%+v err=%v", signedJWT, err)
	}
}

func TestGCSSignedURLsWithOfficialGoLibraryAndIAMClient(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGCSBucket("test-project", "private-assets", "asia-northeast3", "STANDARD"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutGCSObject("private-assets", "reports/hello.txt", []byte("signed content"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(New(store))
	t.Cleanup(httpServer.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := NewGCPGRPCServer(store)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() { grpcServer.Stop(); _ = listener.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	iamClient, err := iamcredentials.NewIamCredentialsClient(ctx,
		option.WithEndpoint(listener.Addr().String()),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer iamClient.Close()
	email := "storage-signer@test-project.iam.gserviceaccount.com"
	signBytes := func(payload []byte) ([]byte, error) {
		response, err := iamClient.SignBlob(ctx, &credentialspb.SignBlobRequest{
			Name: "projects/-/serviceAccounts/" + email, Payload: payload,
		})
		if err != nil {
			return nil, err
		}
		return response.GetSignedBlob(), nil
	}
	hostname := strings.TrimPrefix(httpServer.URL, "http://")
	getURL, err := storage.SignedURL("private-assets", "reports/hello.txt", &storage.SignedURLOptions{
		GoogleAccessID: email, SignBytes: signBytes, Method: http.MethodGet,
		Expires: time.Now().Add(5 * time.Minute), Scheme: storage.SigningSchemeV4, Insecure: true, Hostname: hostname,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(getURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "signed content" {
		t.Fatalf("unexpected signed GET: status=%d body=%q", response.StatusCode, body)
	}

	putURL, err := storage.SignedURL("private-assets", "uploads/new.txt", &storage.SignedURLOptions{
		GoogleAccessID: email, SignBytes: signBytes, Method: http.MethodPut, ContentType: "text/plain",
		Expires: time.Now().Add(5 * time.Minute), Scheme: storage.SigningSchemeV4, Insecure: true, Hostname: hostname,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPut, putURL, strings.NewReader("uploaded with signature"))
	request.Header.Set("Content-Type", "text/plain")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected signed PUT status: %d", response.StatusCode)
	}
	_, uploaded, err := store.GCSObject("private-assets", "uploads/new.txt")
	if err != nil || string(uploaded) != "uploaded with signature" {
		t.Fatalf("unexpected signed PUT object: body=%q err=%v", uploaded, err)
	}

	tampered, _ := url.Parse(getURL)
	tampered.Path = "/private-assets/reports/other.txt"
	response, err = http.Get(tampered.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("tampered URL should be forbidden, got %d", response.StatusCode)
	}
}

func TestGCSSignedPostPolicyWithOfficialGoLibraryAndIAMClient(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(New(store))
	defer httpServer.Close()
	if _, err := store.CreateGCSBucket("test-project", "browser-uploads", "ASIA-NORTHEAST3", "STANDARD"); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := NewGCPGRPCServer(store)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() { grpcServer.Stop(); _ = listener.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	iamClient, err := iamcredentials.NewIamCredentialsClient(ctx,
		option.WithEndpoint(listener.Addr().String()), option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
	if err != nil {
		t.Fatal(err)
	}
	defer iamClient.Close()
	email := "browser-uploader@test-project.iam.gserviceaccount.com"
	sign := func(payload []byte) ([]byte, error) {
		response, err := iamClient.SignBlob(ctx, &credentialspb.SignBlobRequest{Name: "projects/-/serviceAccounts/" + email, Payload: payload})
		if err != nil {
			return nil, err
		}
		return response.GetSignedBlob(), nil
	}
	policy, err := storage.GenerateSignedPostPolicyV4("browser-uploads", "incoming/report.pdf", &storage.PostPolicyV4Options{
		GoogleAccessID: email,
		SignRawBytes:   sign,
		Expires:        time.Now().Add(5 * time.Minute),
		Hostname:       strings.TrimPrefix(httpServer.URL, "http://"),
		Insecure:       true,
		Fields:         &storage.PolicyV4Fields{ContentType: "application/pdf"},
		Conditions:     []storage.PostPolicyV4Condition{storage.ConditionContentLengthRange(1, 1024)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range policy.Fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", "report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("fake pdf")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, policy.URL, &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected POST status %d: %s", response.StatusCode, responseBody)
	}
	_, uploaded, err := store.GCSObject("browser-uploads", "incoming/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if string(uploaded) != "fake pdf" {
		t.Fatalf("unexpected uploaded body: %q", uploaded)
	}
}

func newGCPTestServer(t *testing.T) net.Listener {
	t.Helper()
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := NewGCPGRPCServer(store)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() { grpcServer.Stop(); _ = listener.Close(); _ = store.Close() })
	return listener
}
