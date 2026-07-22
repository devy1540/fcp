package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const metadataPrefix = "/computeMetadata/v1"

func (s *Server) handleMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Metadata-Flavor")), "Google") {
		writeGoogleAPIError(w, http.StatusForbidden, "PERMISSION_DENIED", "Metadata-Flavor: Google header is required")
		return
	}

	w.Header().Set("Metadata-Flavor", "Google")
	switch r.URL.Path {
	case metadataPrefix + "/project/project-id":
		writeMetadataText(w, s.projectID)
	case metadataPrefix + "/instance/service-accounts/default/email":
		writeMetadataText(w, s.serviceAccountEmail)
	case metadataPrefix + "/instance/service-accounts/default/token":
		s.handleMetadataAccessToken(w)
	case metadataPrefix + "/instance/service-accounts/default/identity":
		s.handleMetadataIdentityToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleMetadataAccessToken(w http.ResponseWriter) {
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	claims := map[string]any{
		"iss":   "fcp",
		"sub":   s.serviceAccountEmail,
		"email": s.serviceAccountEmail,
		"scope": "https://www.googleapis.com/auth/cloud-platform",
		"iat":   now.Unix(),
		"exp":   expires.Unix(),
	}
	token, err := newIAMCredentialsServer(s.store).signJWT(s.metadataServiceAccountName(), claims)
	if err != nil {
		writeGoogleAPIError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeGoogleJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"expires_in":   int(time.Until(expires).Seconds()),
		"token_type":   "Bearer",
	})
}

func (s *Server) handleMetadataIdentityToken(w http.ResponseWriter, r *http.Request) {
	audience := strings.TrimSpace(r.URL.Query().Get("audience"))
	if audience == "" {
		writeGoogleAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "audience is required")
		return
	}
	now := time.Now().UTC()
	claims := map[string]any{
		"iss":            "https://accounts.google.com",
		"sub":            s.serviceAccountEmail,
		"aud":            audience,
		"email":          s.serviceAccountEmail,
		"email_verified": true,
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
	}
	token, err := newIAMCredentialsServer(s.store).signJWT(s.metadataServiceAccountName(), claims)
	if err != nil {
		writeGoogleAPIError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeMetadataText(w, token)
}

func (s *Server) handleGoogleJWKS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	account, privateKey, err := newIAMCredentialsServer(s.store).serviceAccount(s.metadataServiceAccountName())
	if err != nil {
		writeGoogleAPIError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeGoogleJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"alg": "RS256",
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes()),
		"kid": account.KeyID,
		"kty": "RSA",
		"n":   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
		"use": "sig",
	}}})
}

func (s *Server) handleSecretManagerREST(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), ":access")
	response, err := newSecretManagerServer(s.store).AccessSecretVersion(context.Background(), &secretmanagerpb.AccessSecretVersionRequest{Name: name})
	if err != nil {
		writeGoogleGRPCError(w, err)
		return
	}
	payload := response.GetPayload()
	writeGoogleJSON(w, http.StatusOK, map[string]any{
		"name": response.GetName(),
		"payload": map[string]string{
			"data":       base64.StdEncoding.EncodeToString(payload.GetData()),
			"dataCrc32c": strconv.FormatInt(payload.GetDataCrc32C(), 10),
		},
	})
}

type kmsRESTRequest struct {
	Plaintext                   string `json:"plaintext"`
	Ciphertext                  string `json:"ciphertext"`
	AdditionalAuthenticatedData string `json:"additionalAuthenticatedData"`
}

func (s *Server) handleKMSREST(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request kmsRESTRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		writeGoogleAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid JSON request body")
		return
	}
	keyName := strings.TrimPrefix(r.URL.Path, "/v1/")
	kms := newKMSServer(s.store)
	additionalData, err := decodeGoogleBase64(request.AdditionalAuthenticatedData)
	if err != nil {
		writeGoogleAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "additionalAuthenticatedData must be base64")
		return
	}

	if strings.HasSuffix(keyName, ":encrypt") {
		keyName = strings.TrimSuffix(keyName, ":encrypt")
		plaintext, err := decodeGoogleBase64(request.Plaintext)
		if err != nil {
			writeGoogleAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "plaintext must be base64")
			return
		}
		response, err := kms.Encrypt(context.Background(), &kmspb.EncryptRequest{
			Name: keyName, Plaintext: plaintext, AdditionalAuthenticatedData: additionalData,
		})
		if err != nil {
			writeGoogleGRPCError(w, err)
			return
		}
		writeGoogleJSON(w, http.StatusOK, map[string]any{
			"name":             response.GetName(),
			"ciphertext":       base64.StdEncoding.EncodeToString(response.GetCiphertext()),
			"ciphertextCrc32c": strconv.FormatInt(response.GetCiphertextCrc32C().GetValue(), 10),
			"protectionLevel":  response.GetProtectionLevel().String(),
		})
		return
	}

	keyName = strings.TrimSuffix(keyName, ":decrypt")
	ciphertext, err := decodeGoogleBase64(request.Ciphertext)
	if err != nil {
		writeGoogleAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "ciphertext must be base64")
		return
	}
	response, err := kms.Decrypt(context.Background(), &kmspb.DecryptRequest{
		Name: keyName, Ciphertext: ciphertext, AdditionalAuthenticatedData: additionalData,
	})
	if err != nil {
		writeGoogleGRPCError(w, err)
		return
	}
	writeGoogleJSON(w, http.StatusOK, map[string]any{
		"plaintext":       base64.StdEncoding.EncodeToString(response.GetPlaintext()),
		"plaintextCrc32c": strconv.FormatInt(response.GetPlaintextCrc32C().GetValue(), 10),
		"protectionLevel": response.GetProtectionLevel().String(),
		"usedPrimary":     response.GetUsedPrimary(),
	})
}

func (s *Server) metadataServiceAccountName() string {
	return "projects/-/serviceAccounts/" + s.serviceAccountEmail
}

func writeMetadataText(w http.ResponseWriter, value string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(value))
}

func writeGoogleJSON(w http.ResponseWriter, statusCode int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeGoogleBase64(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}

func writeGoogleGRPCError(w http.ResponseWriter, err error) {
	grpcStatus := status.Convert(err)
	httpStatus := http.StatusInternalServerError
	switch grpcStatus.Code() {
	case codes.InvalidArgument, codes.FailedPrecondition:
		httpStatus = http.StatusBadRequest
	case codes.NotFound:
		httpStatus = http.StatusNotFound
	case codes.AlreadyExists:
		httpStatus = http.StatusConflict
	case codes.PermissionDenied:
		httpStatus = http.StatusForbidden
	case codes.Unauthenticated:
		httpStatus = http.StatusUnauthorized
	case codes.Unimplemented:
		httpStatus = http.StatusNotImplemented
	}
	writeGoogleAPIError(w, httpStatus, grpcStatus.Code().String(), grpcStatus.Message())
}
