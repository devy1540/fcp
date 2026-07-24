package profile

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hjyoon/fcp/internal/state"
)

type DemoSummary struct {
	Project       string
	Queues        int
	Buckets       int
	Topics        int
	Subscriptions int
	Secrets       int
	KMSKeys       int
	IAMAccounts   int
	DynamoTables  int
}

// SeedDemo creates only local-safe fixtures. It is idempotent and never copies
// values from an external environment.
func SeedDemo(store *state.Store, project string) (DemoSummary, error) {
	if project == "" {
		project = "fcp-local"
	}
	summary := DemoSummary{Project: project}
	if _, err := store.DynamoTable("notifications"); errors.Is(err, state.ErrDynamoTableNotFound) {
		if _, err := store.CreateDynamoTable("notifications",
			[]state.DynamoKeySchemaElement{{AttributeName: "pk", KeyType: "HASH"}, {AttributeName: "sk", KeyType: "RANGE"}},
			[]state.DynamoAttributeDefinition{{AttributeName: "pk", AttributeType: "S"}, {AttributeName: "sk", AttributeType: "S"}},
			"PAY_PER_REQUEST"); err != nil {
			return summary, err
		}
	} else if err != nil {
		return summary, err
	}
	summary.DynamoTables = 1
	for _, queue := range []string{"notifications", "scheduled-jobs"} {
		if _, err := store.CreateQueue(queue, nil); err != nil {
			return summary, err
		}
		summary.Queues++
	}
	for _, bucket := range []string{"assets", "private-assets", "reports", "profiles"} {
		if _, err := store.CreateGCSBucket(project, bucket, "asia-northeast3", "STANDARD"); err != nil {
			return summary, err
		}
		summary.Buckets++
	}

	topicIDs := []string{
		"events", "ai-jobs", "data-pipeline",
		"alerts", "notifications", "jobs",
		"alerts-dlq", "notifications-dlq", "jobs-dlq", "events-dlq", "ai-jobs-dlq",
	}
	for _, id := range topicIDs {
		if _, err := store.CreatePubSubTopic(pubSubTopic(project, id), map[string]string{"fcp-profile": "demo"}); err != nil {
			return summary, err
		}
		summary.Topics++
	}

	type subscriptionSeed struct {
		name, topic, dlq string
	}
	subscriptions := []subscriptionSeed{
		{"data-pipeline-worker", "data-pipeline", ""},
		{"alerts-worker", "alerts", "alerts-dlq"},
		{"notifications-worker", "notifications", "notifications-dlq"},
		{"jobs-worker", "jobs", "jobs-dlq"},
		{"events-worker", "events", "events-dlq"},
		{"ai-jobs-worker", "ai-jobs", "ai-jobs-dlq"},
		{"alerts-dlq-reader", "alerts-dlq", ""},
		{"notifications-dlq-reader", "notifications-dlq", ""},
		{"jobs-dlq-reader", "jobs-dlq", ""},
		{"events-dlq-reader", "events-dlq", ""},
		{"ai-jobs-dlq-reader", "ai-jobs-dlq", ""},
	}
	for _, seed := range subscriptions {
		name := pubSubSubscription(project, seed.name)
		if _, err := store.CreatePubSubSubscription(name, pubSubTopic(project, seed.topic), 10, map[string]string{"fcp-profile": "demo"}, false); err != nil {
			return summary, err
		}
		if seed.dlq != "" {
			if _, err := store.UpdatePubSubSubscription(name, 0, nil, pubSubTopic(project, seed.dlq), 5, false, false, true); err != nil {
				return summary, err
			}
		}
		summary.Subscriptions++
	}

	secrets := map[string]string{
		"app-database":     `{}`,
		"app-jwt":          `{}`,
		"app-ai":           `{}`,
		"app-integrations": `{}`,
		"app-local":        `{}`,
		"notifications":    `{}`,
		"mysql-local":      `{"host":"127.0.0.1","port":"3306","database":"app","username":"app","password":"app"}`,
	}
	for id, payload := range secrets {
		name := fmt.Sprintf("projects/%s/secrets/%s", project, id)
		created, err := store.CreateSecret(name, map[string]string{"fcp-profile": "demo"})
		if err != nil {
			return summary, err
		}
		if len(created.Versions) == 0 {
			if _, err := store.AddSecretVersion(name, []byte(payload)); err != nil {
				return summary, err
			}
		}
		summary.Secrets++
	}

	keyRing := fmt.Sprintf("projects/%s/locations/asia-northeast3/keyRings/fcp-local", project)
	if _, err := store.CreateKMSKeyRing(keyRing); err != nil {
		return summary, err
	}
	jwtName := keyRing + "/cryptoKeys/jwt-signing"
	if _, err := store.KMSCryptoKey(jwtName); err == state.ErrKMSCryptoKeyNotFound {
		privateKey, keyErr := rsa.GenerateKey(rand.Reader, 3072)
		if keyErr != nil {
			return summary, keyErr
		}
		material, keyErr := x509.MarshalPKCS8PrivateKey(privateKey)
		if keyErr != nil {
			return summary, keyErr
		}
		_, err = store.CreateKMSCryptoKey(state.KMSCryptoKey{
			Name: jwtName, Purpose: "ASYMMETRIC_SIGN", Algorithm: "RSA_SIGN_PKCS1_3072_SHA256", PrimaryVersion: 1, CreateTime: time.Now().UTC(),
			Versions: []state.KMSKeyVersion{{Number: 1, Algorithm: "RSA_SIGN_PKCS1_3072_SHA256", State: "ENABLED", KeyMaterial: material, CreateTime: time.Now().UTC()}},
		})
		if err != nil {
			return summary, err
		}
	}
	symmetricName := keyRing + "/cryptoKeys/data-encryption"
	if _, err := store.KMSCryptoKey(symmetricName); err == state.ErrKMSCryptoKeyNotFound {
		material := make([]byte, 32)
		if _, err := rand.Read(material); err != nil {
			return summary, err
		}
		now := time.Now().UTC()
		if _, err := store.CreateKMSCryptoKey(state.KMSCryptoKey{
			Name: symmetricName, Purpose: "ENCRYPT_DECRYPT", Algorithm: "GOOGLE_SYMMETRIC_ENCRYPTION", PrimaryVersion: 1, CreateTime: now,
			Versions: []state.KMSKeyVersion{{Number: 1, Algorithm: "GOOGLE_SYMMETRIC_ENCRYPTION", State: "ENABLED", KeyMaterial: material, CreateTime: now}},
		}); err != nil {
			return summary, err
		}
	}
	summary.KMSKeys = 2
	if _, err := demoStorageSigner(store, project); err != nil {
		return summary, err
	}
	summary.IAMAccounts = 1
	return summary, nil
}

// WriteDemoCredentials exports a local-only service-account key backed by the
// same persistent IAM signing key FCP uses to verify GCS signed requests.
func WriteDemoCredentials(store *state.Store, project, path string) error {
	if project == "" {
		project = "fcp-local"
	}
	account, err := demoStorageSigner(store, project)
	if err != nil {
		return err
	}
	credentials := struct {
		Type         string `json:"type"`
		ProjectID    string `json:"project_id"`
		PrivateKeyID string `json:"private_key_id"`
		PrivateKey   string `json:"private_key"`
		ClientEmail  string `json:"client_email"`
		TokenURI     string `json:"token_uri"`
	}{
		Type:         "service_account",
		ProjectID:    project,
		PrivateKeyID: account.KeyID,
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: account.PrivateKey,
		})),
		ClientEmail: demoStorageSignerEmail(project),
		TokenURI:    "https://oauth2.googleapis.com/token",
	}
	payload, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func demoStorageSigner(store *state.Store, project string) (state.IAMServiceAccount, error) {
	name := "projects/-/serviceAccounts/" + demoStorageSignerEmail(project)
	return store.IAMServiceAccount(name, func() ([]byte, error) {
		privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		return x509.MarshalPKCS8PrivateKey(privateKey)
	})
}

func demoStorageSignerEmail(project string) string {
	return fmt.Sprintf("fcp-storage-signer@%s.iam.gserviceaccount.com", project)
}

func pubSubTopic(project, id string) string {
	return fmt.Sprintf("projects/%s/topics/%s", project, id)
}

func pubSubSubscription(project, id string) string {
	return fmt.Sprintf("projects/%s/subscriptions/%s", project, id)
}
