package server

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hjyoon/fcp/internal/profile"
	"github.com/hjyoon/fcp/internal/state"
)

func newDemoRESTTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profile.SeedDemo(store, "fcp-local"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewWithOptions(store, Options{
		ProjectID:           "fcp-local",
		ServiceAccountEmail: "app@fcp-local.iam.gserviceaccount.com",
	}))
	t.Cleanup(server.Close)
	return server
}

func TestMetadataServerTokenIdentityAndJWKS(t *testing.T) {
	server := newDemoRESTTestServer(t)

	missingHeader, err := http.Get(server.URL + metadataPrefix + "/project/project-id")
	if err != nil {
		t.Fatal(err)
	}
	missingHeader.Body.Close()
	if missingHeader.StatusCode != http.StatusForbidden {
		t.Fatalf("metadata request without flavor header status = %d", missingHeader.StatusCode)
	}

	projectID := metadataGet(t, server.URL+metadataPrefix+"/project/project-id", false)
	if projectID != "fcp-local" {
		t.Fatalf("metadata project ID = %q", projectID)
	}
	email := metadataGet(t, server.URL+metadataPrefix+"/instance/service-accounts/default/email", false)
	if email != "app@fcp-local.iam.gserviceaccount.com" {
		t.Fatalf("metadata service account = %q", email)
	}

	var accessToken struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	decodeJSON(t, []byte(metadataGet(t, server.URL+metadataPrefix+"/instance/service-accounts/default/token", false)), &accessToken)
	if len(strings.Split(accessToken.AccessToken, ".")) != 3 || accessToken.ExpiresIn <= 0 || accessToken.TokenType != "Bearer" {
		t.Fatalf("unexpected metadata access token response: %+v", accessToken)
	}

	identity := metadataGet(t, server.URL+metadataPrefix+"/instance/service-accounts/default/identity?audience=fcp-test-audience&format=full", false)
	parts := strings.Split(identity, ".")
	if len(parts) != 3 {
		t.Fatalf("identity token has %d parts", len(parts))
	}
	var header map[string]any
	var claims map[string]any
	decodeJWTPart(t, parts[0], &header)
	decodeJWTPart(t, parts[1], &claims)
	if claims["iss"] != "https://accounts.google.com" || claims["aud"] != "fcp-test-audience" || claims["email"] != email || claims["email_verified"] != true {
		t.Fatalf("unexpected identity claims: %+v", claims)
	}

	response, err := http.Get(server.URL + "/oauth2/v3/certs")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(response.Body).Decode(&jwks); err != nil {
		t.Fatal(err)
	}
	if len(jwks.Keys) != 1 || jwks.Keys[0].Kid != header["kid"] {
		t.Fatalf("unexpected JWKS: %+v", jwks)
	}
	modulus := decodeRawURLBase64(t, jwks.Keys[0].N)
	exponent := decodeRawURLBase64(t, jwks.Keys[0].E)
	publicKey := &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(new(big.Int).SetBytes(exponent).Int64())}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], decodeRawURLBase64(t, parts[2])); err != nil {
		t.Fatalf("identity token signature verification failed: %v", err)
	}
}

func TestSecretManagerRESTAccess(t *testing.T) {
	server := newDemoRESTTestServer(t)
	response, err := http.Get(server.URL + "/v1/projects/fcp-local/secrets/notifications/versions/latest:access")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("secret access status = %d: %s", response.StatusCode, body)
	}
	var result struct {
		Name    string `json:"name"`
		Payload struct {
			Data string `json:"data"`
		} `json:"payload"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.Payload.Data)
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "projects/fcp-local/secrets/notifications/versions/1" || string(decoded) != `{}` {
		t.Fatalf("unexpected secret REST response: name=%q payload=%q", result.Name, decoded)
	}
}

func TestKMSRESTEncryptAndDecrypt(t *testing.T) {
	server := newDemoRESTTestServer(t)
	key := "projects/fcp-local/locations/asia-northeast3/keyRings/fcp-local/cryptoKeys/data-encryption"
	plaintext := []byte("demo data")
	encryptBody, _ := json.Marshal(map[string]string{"plaintext": base64.StdEncoding.EncodeToString(plaintext)})
	encryptResponse := postJSON(t, server.URL+"/v1/"+key+":encrypt", encryptBody)
	var encrypted struct {
		Ciphertext string `json:"ciphertext"`
	}
	decodeJSON(t, encryptResponse, &encrypted)
	if encrypted.Ciphertext == "" {
		t.Fatal("KMS REST ciphertext is empty")
	}

	decryptBody, _ := json.Marshal(map[string]string{"ciphertext": encrypted.Ciphertext})
	decryptResponse := postJSON(t, server.URL+"/v1/"+key+":decrypt", decryptBody)
	var decrypted struct {
		Plaintext   string `json:"plaintext"`
		UsedPrimary bool   `json:"usedPrimary"`
	}
	decodeJSON(t, decryptResponse, &decrypted)
	decoded, err := base64.StdEncoding.DecodeString(decrypted.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, plaintext) || !decrypted.UsedPrimary {
		t.Fatalf("unexpected KMS REST plaintext=%q usedPrimary=%t", decoded, decrypted.UsedPrimary)
	}
}

func metadataGet(t *testing.T, url string, rawJSON bool) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Metadata-Flavor", "Google")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("metadata GET %s status = %d: %s", url, response.StatusCode, body)
	}
	if response.Header.Get("Metadata-Flavor") != "Google" {
		t.Fatal("metadata response is missing Metadata-Flavor header")
	}
	if rawJSON {
		return string(body)
	}
	return strings.TrimSpace(string(body))
}

func postJSON(t *testing.T, url string, body []byte) []byte {
	t.Helper()
	response, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d: %s", url, response.StatusCode, result)
	}
	return result
}

func decodeJSON(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode JSON %q: %v", body, err)
	}
}

func decodeJWTPart(t *testing.T, value string, target any) {
	t.Helper()
	decodeJSON(t, decodeRawURLBase64(t, value), target)
}

func decodeRawURLBase64(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
