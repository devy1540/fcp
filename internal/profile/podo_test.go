package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hjyoon/fcp/internal/state"
)

func TestSeedPodoIsIdempotent(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		summary, err := SeedPodo(store, "podo-local")
		if err != nil {
			t.Fatal(err)
		}
		if summary.Queues != 2 || summary.Buckets != 4 || summary.Topics != 11 || summary.Subscriptions != 11 || summary.Secrets != 7 || summary.KMSKeys != 2 || summary.IAMAccounts != 1 || summary.DynamoTables != 1 {
			t.Fatalf("unexpected summary: %+v", summary)
		}
	}
	for _, queueName := range []string{"notification-local", "reserved-local"} {
		queue, err := store.Queue(queueName)
		if err != nil || queue.Name != queueName {
			t.Fatalf("unexpected PODO SQS queue %q: queue=%+v err=%v", queueName, queue, err)
		}
	}
	dynamoTable, err := store.DynamoTable("podo-notification")
	if err != nil || len(dynamoTable.KeySchema) != 2 || dynamoTable.BillingMode != "PAY_PER_REQUEST" {
		t.Fatalf("unexpected PODO DynamoDB table: table=%+v err=%v", dynamoTable, err)
	}
	secret, err := store.Secret("projects/podo-local/secrets/podo-common")
	if err != nil {
		t.Fatal(err)
	}
	if len(secret.Versions) != 1 {
		t.Fatalf("profile restart must not add secret versions: %+v", secret.Versions)
	}
	subscription, err := store.PubSubSubscription("projects/podo-local/subscriptions/podo-slack-dev-sub")
	if err != nil {
		t.Fatal(err)
	}
	if subscription.DeadLetterTopic != "projects/podo-local/topics/podo-slack-dlq" || subscription.MaxDeliveryAttempts != 5 {
		t.Fatalf("unexpected PODO DLQ policy: %+v", subscription)
	}
	credentialsPath := filepath.Join(t.TempDir(), "credentials.json")
	if err := WritePodoCredentials(store, "podo-local", credentialsPath); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(credentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials permissions = %o, want 600", info.Mode().Perm())
	}
	var credentials struct {
		ProjectID    string `json:"project_id"`
		ClientEmail  string `json:"client_email"`
		PrivateKeyID string `json:"private_key_id"`
		PrivateKey   string `json:"private_key"`
	}
	payload, err := os.ReadFile(credentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &credentials); err != nil {
		t.Fatal(err)
	}
	if credentials.ProjectID != "podo-local" || credentials.ClientEmail != "podo-storage-signer@podo-local.iam.gserviceaccount.com" || credentials.PrivateKeyID == "" || credentials.PrivateKey == "" {
		t.Fatalf("unexpected local credentials: %+v", credentials)
	}
}
