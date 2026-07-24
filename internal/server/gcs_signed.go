package server

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/devy1540/fcp/internal/state"
)

const gcsV4Algorithm = "GOOG4-RSA-SHA256"

func (s *Server) handleGCSSignedRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handleGCSSignedPost(w, r)
		return
	}
	if err := s.verifyGCSV4SignedURL(r, time.Now().UTC()); err != nil {
		gcsError(w, http.StatusForbidden, "SignatureDoesNotMatch", err.Error())
		return
	}
	bucket, object, ok := signedGCSObjectPath(r.URL.EscapedPath())
	if !ok {
		gcsError(w, http.StatusBadRequest, "invalid", "signed URL must target /bucket/object")
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		stored, body, err := s.store.GCSObject(bucket, object)
		if errors.Is(err, state.ErrGCSBucketNotFound) || errors.Is(err, state.ErrGCSObjectNotFound) {
			gcsNotFound(w, "Object", object)
			return
		}
		if err != nil {
			gcsInternalError(w, err)
			return
		}
		writeGCSMedia(w, r, stored, body)
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			gcsError(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		stored, err := s.store.PutGCSObject(bucket, object, body, r.Header.Get("Content-Type"), nil)
		if errors.Is(err, state.ErrGCSBucketNotFound) {
			gcsNotFound(w, "Bucket", bucket)
			return
		}
		if err != nil {
			gcsInternalError(w, err)
			return
		}
		w.Header().Set("ETag", stored.ETag)
		w.Header().Set("x-goog-generation", strconv.FormatInt(stored.Generation, 10))
		w.WriteHeader(http.StatusOK)
	default:
		gcsError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "signed request method is not supported")
	}
}

func (s *Server) verifyGCSV4SignedURL(r *http.Request, now time.Time) error {
	query := r.URL.Query()
	if query.Get("X-Goog-Algorithm") != gcsV4Algorithm {
		return errors.New("unsupported signing algorithm")
	}
	credential := query.Get("X-Goog-Credential")
	parts := strings.SplitN(credential, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		return errors.New("invalid signing credential")
	}
	signedAt, err := time.Parse("20060102T150405Z", query.Get("X-Goog-Date"))
	if err != nil {
		return errors.New("invalid signing date")
	}
	expires, err := strconv.ParseInt(query.Get("X-Goog-Expires"), 10, 64)
	if err != nil || expires < 1 || expires > 604800 {
		return errors.New("invalid signed URL expiration")
	}
	if now.After(signedAt.Add(time.Duration(expires) * time.Second)) {
		return errors.New("signed URL has expired")
	}
	if signedAt.After(now.Add(15 * time.Minute)) {
		return errors.New("signed URL date is too far in the future")
	}
	signedHeaders := query.Get("X-Goog-SignedHeaders")
	canonicalHeaders, err := canonicalGCSHeaders(r, signedHeaders, canonicalGCSHost(r.Host))
	if err != nil {
		return err
	}
	canonicalQuery := cloneURLValues(query)
	canonicalQuery.Del("X-Goog-Signature")
	payloadHash := "UNSIGNED-PAYLOAD"
	if strings.Contains(signedHeaders, "x-goog-content-sha256") && r.Header.Get("x-goog-content-sha256") != "" {
		payloadHash = r.Header.Get("x-goog-content-sha256")
	}
	signature, err := hex.DecodeString(query.Get("X-Goog-Signature"))
	if err != nil {
		return errors.New("invalid signature encoding")
	}
	verify := func(headers string) error {
		canonicalRequest := strings.Join([]string{
			r.Method,
			canonicalPath(r.URL),
			strings.ReplaceAll(canonicalQuery.Encode(), "+", "%20"),
			headers,
			signedHeaders,
			payloadHash,
		}, "\n")
		requestHash := sha256.Sum256([]byte(canonicalRequest))
		stringToSign := gcsV4Algorithm + "\n" + query.Get("X-Goog-Date") + "\n" + parts[1] + "\n" + hex.EncodeToString(requestHash[:])
		return s.verifyIAMSignature(parts[0], []byte(stringToSign), signature)
	}
	if err := verify(canonicalHeaders); err == nil {
		return nil
	}
	// Java Storage preserves a custom emulator port in its canonical host,
	// while Go Storage canonicalizes the same host without the port.
	if r.Host != canonicalGCSHost(r.Host) {
		alternate, headerErr := canonicalGCSHeaders(r, signedHeaders, r.Host)
		if headerErr == nil && verify(alternate) == nil {
			return nil
		}
	}
	return errors.New("signature verification failed")
}

func canonicalGCSHeaders(r *http.Request, signedHeaders, host string) (string, error) {
	if signedHeaders == "" {
		return "", errors.New("signed headers are required")
	}
	var builder strings.Builder
	for _, name := range strings.Split(signedHeaders, ";") {
		name = strings.ToLower(strings.TrimSpace(name))
		var values []string
		if name == "host" {
			values = []string{host}
		} else {
			values = r.Header.Values(name)
		}
		if len(values) == 0 {
			return "", fmt.Errorf("signed header %q is missing", name)
		}
		for i := range values {
			values[i] = strings.Join(strings.Fields(values[i]), " ")
		}
		builder.WriteString(name)
		builder.WriteByte(':')
		builder.WriteString(strings.Join(values, ","))
		builder.WriteByte('\n')
	}
	return builder.String(), nil
}

func canonicalGCSHost(host string) string {
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		return parsed
	}
	return strings.Trim(host, "[]")
}

func canonicalPath(value *url.URL) string {
	if value.EscapedPath() == "" {
		return "/"
	}
	return value.EscapedPath()
}

func cloneURLValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, value := range values {
		cloned[key] = append([]string(nil), value...)
	}
	return cloned
}

func signedGCSObjectPath(path string) (string, string, bool) {
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	bucket, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", false
	}
	object, err := url.PathUnescape(parts[1])
	if err != nil {
		return "", "", false
	}
	return bucket, object, true
}

func (s *Server) verifyIAMSignature(email string, payload, signature []byte) error {
	name := "projects/-/serviceAccounts/" + email
	account, err := s.store.ExistingIAMServiceAccount(name)
	if err != nil {
		return errors.New("unknown signing service account")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(account.PrivateKey)
	if err != nil {
		return errors.New("invalid signing service account key")
	}
	privateKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return errors.New("signing service account key is not RSA")
	}
	digest := sha256.Sum256(payload)
	if err := rsa.VerifyPKCS1v15(&privateKey.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		return errors.New("signature verification failed")
	}
	return nil
}

type gcsPostPolicy struct {
	Expiration time.Time `json:"expiration"`
	Conditions []any     `json:"conditions"`
}

func (s *Server) handleGCSSignedPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		gcsError(w, http.StatusBadRequest, "invalid", "invalid signed POST form")
		return
	}
	credential := r.FormValue("x-goog-credential")
	parts := strings.SplitN(credential, "/", 2)
	if len(parts) != 2 {
		gcsError(w, http.StatusForbidden, "SignatureDoesNotMatch", "invalid signing credential")
		return
	}
	policyBase64 := r.FormValue("policy")
	signature, err := hex.DecodeString(r.FormValue("x-goog-signature"))
	if err != nil || s.verifyIAMSignature(parts[0], []byte(policyBase64), signature) != nil {
		gcsError(w, http.StatusForbidden, "SignatureDoesNotMatch", "policy signature verification failed")
		return
	}
	rawPolicy, err := base64.StdEncoding.DecodeString(policyBase64)
	if err != nil {
		gcsError(w, http.StatusBadRequest, "invalid", "invalid policy encoding")
		return
	}
	var policy gcsPostPolicy
	if err := json.Unmarshal(rawPolicy, &policy); err != nil || time.Now().UTC().After(policy.Expiration) {
		gcsError(w, http.StatusForbidden, "AccessDenied", "POST policy is invalid or expired")
		return
	}
	bucket := strings.Trim(r.URL.Path, "/")
	if bucket == "" {
		bucket = gcsPostPolicyBucket(policy.Conditions)
	}
	key := r.FormValue("key")
	file, header, err := r.FormFile("file")
	if err != nil {
		gcsError(w, http.StatusBadRequest, "invalid", "file field is required")
		return
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		gcsError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if err := validateGCSPostConditions(policy.Conditions, r.MultipartForm, bucket, key, int64(len(body))); err != nil {
		gcsError(w, http.StatusForbidden, "AccessDenied", err.Error())
		return
	}
	contentType := r.FormValue("Content-Type")
	if contentType == "" {
		contentType = header.Header.Get("Content-Type")
	}
	stored, err := s.store.PutGCSObject(bucket, key, body, contentType, nil)
	if errors.Is(err, state.ErrGCSBucketNotFound) {
		gcsNotFound(w, "Bucket", bucket)
		return
	}
	if err != nil {
		gcsInternalError(w, err)
		return
	}
	w.Header().Set("ETag", stored.ETag)
	w.WriteHeader(http.StatusNoContent)
}

func gcsPostPolicyBucket(conditions []any) string {
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if bucket := fmt.Sprint(condition["bucket"]); bucket != "<nil>" && bucket != "" {
			return bucket
		}
	}
	return ""
}

func validateGCSPostConditions(conditions []any, form *multipart.Form, bucket, key string, size int64) error {
	values := form.Value
	for _, raw := range conditions {
		switch condition := raw.(type) {
		case map[string]any:
			for field, expected := range condition {
				actual := firstFormValue(values, field)
				if field == "bucket" {
					actual = bucket
				}
				if actual != fmt.Sprint(expected) {
					return fmt.Errorf("POST policy field %q does not match", field)
				}
			}
		case []any:
			if len(condition) < 3 {
				continue
			}
			op := fmt.Sprint(condition[0])
			if op == "content-length-range" {
				minimum, _ := strconv.ParseInt(fmt.Sprint(condition[1]), 10, 64)
				maximum, _ := strconv.ParseInt(fmt.Sprint(condition[2]), 10, 64)
				if size < minimum || size > maximum {
					return errors.New("POST upload size is outside the policy range")
				}
			}
			if op == "starts-with" {
				field := strings.TrimPrefix(fmt.Sprint(condition[1]), "$")
				actual := firstFormValue(values, field)
				if field == "key" {
					actual = key
				}
				if !strings.HasPrefix(actual, fmt.Sprint(condition[2])) {
					return fmt.Errorf("POST policy field %q has an invalid prefix", field)
				}
			}
		}
	}
	return nil
}

func firstFormValue(values map[string][]string, key string) string {
	if value := values[key]; len(value) > 0 {
		return value[0]
	}
	for candidate, value := range values {
		if strings.EqualFold(candidate, key) && len(value) > 0 {
			return value[0]
		}
	}
	return ""
}
