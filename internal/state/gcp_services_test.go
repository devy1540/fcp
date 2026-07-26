package state

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestFirestoreAndSecretStateLifecycle(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	documentName := "projects/test-project/databases/(default)/documents/config/current"
	if err := store.MutateFirestore(func(documents map[string]*FirestoreDocument, now time.Time) error {
		documents[documentName] = &FirestoreDocument{Name: documentName, Proto: []byte("wire"), CreateTime: now, UpdateTime: now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	document, err := store.FirestoreDocument(documentName)
	if err != nil || !bytes.Equal(document.Proto, []byte("wire")) {
		t.Fatalf("unexpected Firestore document: document=%+v err=%v", document, err)
	}
	document.Proto[0] = 'X'
	document, err = store.FirestoreDocument(documentName)
	if err != nil || !bytes.Equal(document.Proto, []byte("wire")) {
		t.Fatalf("Firestore document was not cloned: document=%+v err=%v", document, err)
	}
	mutationError := errors.New("reject mutation")
	if err := store.MutateFirestore(func(documents map[string]*FirestoreDocument, _ time.Time) error {
		delete(documents, documentName)
		return mutationError
	}); !errors.Is(err, mutationError) {
		t.Fatalf("mutation error was not propagated: %v", err)
	}
	if documents := store.ListFirestoreDocuments("projects/test-project/"); len(documents) != 1 || documents[0].Name != documentName {
		t.Fatalf("failed mutation changed Firestore state: %+v", documents)
	}

	parent := "projects/test-project"
	secretName := parent + "/secrets/backend"
	secret, err := store.CreateSecret(secretName, map[string]string{"env": "dev"})
	if err != nil || secret.Labels["env"] != "dev" {
		t.Fatalf("unexpected secret: secret=%+v err=%v", secret, err)
	}
	updated, err := store.UpdateSecretLabels(secretName, map[string]string{"env": "test"})
	if err != nil || updated.Labels["env"] != "test" {
		t.Fatalf("secret labels were not updated: secret=%+v err=%v", updated, err)
	}
	if secrets := store.ListSecrets(parent); len(secrets) != 1 || secrets[0].Name != secretName {
		t.Fatalf("unexpected secret list: %+v", secrets)
	}
	version, err := store.AddSecretVersion(secretName, []byte("payload"))
	if err != nil || version.Number != 1 {
		t.Fatalf("unexpected secret version: version=%+v err=%v", version, err)
	}
	latest, err := store.SecretVersion(secretName, 0)
	if err != nil || !bytes.Equal(latest.Payload, []byte("payload")) {
		t.Fatalf("unexpected latest secret version: version=%+v err=%v", latest, err)
	}
	disabled, err := store.SetSecretVersionState(secretName, 1, "DISABLED")
	if err != nil || disabled.State != "DISABLED" {
		t.Fatalf("unexpected disabled version: version=%+v err=%v", disabled, err)
	}
	if _, err := store.SecretVersion(secretName, 0); !errors.Is(err, ErrSecretVersionNotFound) {
		t.Fatalf("latest should skip disabled versions: %v", err)
	}
	destroyed, err := store.SetSecretVersionState(secretName, 1, "DESTROYED")
	if err != nil || destroyed.State != "DESTROYED" || len(destroyed.Payload) != 0 {
		t.Fatalf("unexpected destroyed version: version=%+v err=%v", destroyed, err)
	}
	if err := store.DeleteSecret(secretName); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Secret(secretName); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("deleted secret should be missing: %v", err)
	}
}

func TestKMSAndIAMStateLifecycle(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	keyRingName := "projects/test-project/locations/global/keyRings/test"
	keyRing, err := store.CreateKMSKeyRing(keyRingName)
	if err != nil || keyRing.Name != keyRingName {
		t.Fatalf("unexpected key ring: keyRing=%+v err=%v", keyRing, err)
	}
	if rings := store.ListKMSKeyRings("projects/test-project/locations/global"); len(rings) != 1 || rings[0].Name != keyRingName {
		t.Fatalf("unexpected key ring list: %+v", rings)
	}
	keyName := keyRingName + "/cryptoKeys/data"
	if _, err := store.CreateKMSCryptoKey(KMSCryptoKey{Name: "invalid"}); !errors.Is(err, ErrKMSInvalidName) {
		t.Fatalf("invalid crypto key name should fail without panicking: %v", err)
	}
	key, err := store.CreateKMSCryptoKey(KMSCryptoKey{
		Name: keyName, Purpose: "ENCRYPT_DECRYPT", Algorithm: "GOOGLE_SYMMETRIC_ENCRYPTION", CreateTime: time.Now().UTC(),
	})
	if err != nil || key.Name != keyName {
		t.Fatalf("unexpected crypto key: key=%+v err=%v", key, err)
	}
	if keys := store.ListKMSCryptoKeys(keyRingName); len(keys) != 1 || keys[0].Name != keyName {
		t.Fatalf("unexpected crypto key list: %+v", keys)
	}
	version, err := store.AddKMSKeyVersion(keyName, "GOOGLE_SYMMETRIC_ENCRYPTION", []byte("local-key-material"))
	if err != nil || version.Number != 1 {
		t.Fatalf("unexpected KMS version: version=%+v err=%v", version, err)
	}
	latest, err := store.KMSKeyVersion(keyName, 0)
	if err != nil || latest.Number != 1 || !bytes.Equal(latest.KeyMaterial, []byte("local-key-material")) {
		t.Fatalf("unexpected latest KMS version: version=%+v err=%v", latest, err)
	}
	latest.KeyMaterial[0] = 'X'
	again, err := store.KMSKeyVersion(keyName, 1)
	if err != nil || !bytes.Equal(again.KeyMaterial, []byte("local-key-material")) {
		t.Fatalf("KMS key material was not cloned: version=%+v err=%v", again, err)
	}

	accountName := "projects/-/serviceAccounts/fcp@test-project.iam.gserviceaccount.com"
	account, err := store.IAMServiceAccount(accountName, func() ([]byte, error) {
		return []byte("local-private-key"), nil
	})
	if err != nil || account.Name != accountName || account.KeyID == "" {
		t.Fatalf("unexpected IAM account: account=%+v err=%v", account, err)
	}
	account.PrivateKey[0] = 'X'
	existing, err := store.ExistingIAMServiceAccount(accountName)
	if err != nil || !bytes.Equal(existing.PrivateKey, []byte("local-private-key")) {
		t.Fatalf("IAM private key was not cloned: account=%+v err=%v", existing, err)
	}
	if accounts := store.ListIAMServiceAccounts(); len(accounts) != 1 || accounts[0].Name != accountName {
		t.Fatalf("unexpected IAM account list: %+v", accounts)
	}
	if _, err := store.ExistingIAMServiceAccount("missing"); !errors.Is(err, ErrIAMServiceAccountNotFound) {
		t.Fatalf("missing IAM account should fail: %v", err)
	}
}
