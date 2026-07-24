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
	iamcredentials "cloud.google.com/go/iam/credentials/apiv1"
	"cloud.google.com/go/iam/credentials/apiv1/credentialspb"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"cloud.google.com/go/storage"
	"github.com/hjyoon/fcp/internal/state"
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
	t.Cleanup(func() { grpcServer.Stop(); _ = listener.Close() })
	return listener
}
