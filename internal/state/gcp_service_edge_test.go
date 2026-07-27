package state

import (
	"errors"
	"testing"
)

func TestSecretVersionAndLabelErrorEdges(t *testing.T) {
	store := openEdgeStore(t)
	name := "projects/test/secrets/api-key"
	if _, err := store.Secret("missing"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("missing secret lookup error=%v", err)
	}
	if _, err := store.UpdateSecretLabels("missing", nil); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("missing secret update error=%v", err)
	}
	if err := store.DeleteSecret("missing"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("missing secret delete error=%v", err)
	}
	if _, err := store.AddSecretVersion("missing", nil); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("missing secret version add error=%v", err)
	}
	if _, err := store.SecretVersion("missing", 1); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("missing secret version lookup error=%v", err)
	}
	if _, err := store.SetSecretVersionState("missing", 1, "DISABLED"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("missing secret version state error=%v", err)
	}

	created, err := store.CreateSecret(name, map[string]string{"env": "test"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.CreateSecret(name, map[string]string{"env": "changed"})
	if err != nil || duplicate.Labels["env"] != created.Labels["env"] {
		t.Fatalf("duplicate secret should preserve labels: %+v err=%v", duplicate, err)
	}
	if _, err := store.SecretVersion(name, 0); !errors.Is(err, ErrSecretVersionNotFound) {
		t.Fatalf("latest secret without versions error=%v", err)
	}
	first, err := store.AddSecretVersion(name, []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddSecretVersion(name, []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetSecretVersionState(name, second.Number, "DISABLED"); err != nil {
		t.Fatal(err)
	}
	latest, err := store.SecretVersion(name, 0)
	if err != nil || latest.Number != first.Number {
		t.Fatalf("latest should skip disabled version: %+v err=%v", latest, err)
	}
	destroyed, err := store.SetSecretVersionState(name, first.Number, "DESTROYED")
	if err != nil || destroyed.Payload != nil {
		t.Fatalf("destroyed version should clear payload: %+v err=%v", destroyed, err)
	}
	if _, err := store.SetSecretVersionState(name, 99, "DISABLED"); !errors.Is(err, ErrSecretVersionNotFound) {
		t.Fatalf("missing version state error=%v", err)
	}
	if _, err := store.SecretVersion(name, 99); !errors.Is(err, ErrSecretVersionNotFound) {
		t.Fatalf("missing version lookup error=%v", err)
	}
}

func TestKMSCryptoKeyVersionAndIAMErrorEdges(t *testing.T) {
	store := openEdgeStore(t)
	ring := "projects/test/locations/global/keyRings/main"
	keyName := ring + "/cryptoKeys/data"
	if _, err := store.CreateKMSCryptoKey(KMSCryptoKey{Name: "invalid"}); !errors.Is(err, ErrKMSInvalidName) {
		t.Fatalf("invalid crypto key name error=%v", err)
	}
	if _, err := store.CreateKMSCryptoKey(KMSCryptoKey{Name: keyName}); !errors.Is(err, ErrKMSKeyRingNotFound) {
		t.Fatalf("missing crypto key ring error=%v", err)
	}
	if _, err := store.KMSCryptoKey("missing"); !errors.Is(err, ErrKMSCryptoKeyNotFound) {
		t.Fatalf("missing crypto key lookup error=%v", err)
	}
	if _, err := store.AddKMSKeyVersion("missing", "algorithm", nil); !errors.Is(err, ErrKMSCryptoKeyNotFound) {
		t.Fatalf("missing crypto key version add error=%v", err)
	}
	if _, err := store.KMSKeyVersion("missing", 1); !errors.Is(err, ErrKMSCryptoKeyNotFound) {
		t.Fatalf("missing crypto key version lookup error=%v", err)
	}
	if _, err := store.CreateKMSKeyRing(ring); err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKMSCryptoKey(KMSCryptoKey{Name: keyName, Purpose: "ENCRYPT_DECRYPT"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.CreateKMSCryptoKey(KMSCryptoKey{Name: keyName, Purpose: "ASYMMETRIC_SIGN"})
	if err != nil || duplicate.Purpose != key.Purpose {
		t.Fatalf("duplicate crypto key should preserve purpose: %+v err=%v", duplicate, err)
	}
	if _, err := store.KMSKeyVersion(keyName, 0); !errors.Is(err, ErrKMSKeyVersionMissing) {
		t.Fatalf("primary version before creation error=%v", err)
	}
	version, err := store.AddKMSKeyVersion(keyName, "GOOGLE_SYMMETRIC_ENCRYPTION", []byte("material"))
	if err != nil {
		t.Fatal(err)
	}
	primary, err := store.KMSKeyVersion(keyName, 0)
	if err != nil || primary.Number != version.Number {
		t.Fatalf("primary version lookup failed: %+v err=%v", primary, err)
	}
	if _, err := store.KMSKeyVersion(keyName, 99); !errors.Is(err, ErrKMSKeyVersionMissing) {
		t.Fatalf("missing key version error=%v", err)
	}
	if got := store.ListKMSCryptoKeys("projects/other/locations/global/keyRings/main"); len(got) != 0 {
		t.Fatalf("KMS key parent filter failed: %+v", got)
	}

	if _, err := store.ExistingIAMServiceAccount("missing"); !errors.Is(err, ErrIAMServiceAccountNotFound) {
		t.Fatalf("missing IAM account error=%v", err)
	}
	generateFailure := errors.New("generate failed")
	if _, err := store.IAMServiceAccount("account", func() ([]byte, error) { return nil, generateFailure }); !errors.Is(err, generateFailure) {
		t.Fatalf("IAM generator error=%v", err)
	}
	generated := 0
	account, err := store.IAMServiceAccount("account", func() ([]byte, error) {
		generated++
		return []byte("private"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.IAMServiceAccount("account", func() ([]byte, error) {
		generated++
		return []byte("other"), nil
	})
	if err != nil || generated != 1 || string(again.PrivateKey) != string(account.PrivateKey) {
		t.Fatalf("existing IAM account should skip generation: %+v generated=%d err=%v", again, generated, err)
	}
}
