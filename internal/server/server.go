package server

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/devy1540/fcp/internal/state"
)

type Server struct {
	store               *state.Store
	gcsUploads          sync.Map
	projectID           string
	serviceAccountEmail string
}

type Options struct {
	ProjectID           string
	ServiceAccountEmail string
}

func New(store *state.Store) http.Handler {
	return NewWithOptions(store, Options{})
}

func NewWithOptions(store *state.Store, options Options) http.Handler {
	projectID := strings.TrimSpace(options.ProjectID)
	if projectID == "" {
		projectID = "fcp-local"
	}
	serviceAccountEmail := strings.TrimSpace(options.ServiceAccountEmail)
	if serviceAccountEmail == "" {
		serviceAccountEmail = "fcp-default@" + projectID + ".iam.gserviceaccount.com"
	}
	return &Server{store: store, projectID: projectID, serviceAccountEmail: serviceAccountEmail}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		log.Printf("%s %s %d %s", r.Method, r.URL.RequestURI(), recorder.status, time.Since(started).Round(time.Millisecond))
	}()
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") && r.Body != nil {
		reader, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(recorder, "invalid gzip request body", http.StatusBadRequest)
			return
		}
		original := r.Body
		r.Body = struct {
			io.Reader
			io.Closer
		}{Reader: reader, Closer: original}
		r.Header.Del("Content-Encoding")
		r.ContentLength = -1
		defer reader.Close()
	}

	if r.URL.Path == "/b" || strings.HasPrefix(r.URL.Path, "/b/") {
		r.URL.Path = "/storage/v1" + r.URL.Path
		if r.URL.RawPath != "" {
			r.URL.RawPath = "/storage/v1" + r.URL.RawPath
		}
	}
	if strings.HasPrefix(r.URL.Path, "/storage/v1/") || strings.HasPrefix(r.URL.Path, "/upload/storage/v1/") || strings.HasPrefix(r.URL.Path, "/download/storage/v1/") || strings.HasPrefix(r.URL.Path, "/_fcp/gcs-upload/") {
		s.handleGCS(recorder, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/_fcp/") {
		s.handleAdmin(recorder, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/computeMetadata/v1/") {
		s.handleMetadata(recorder, r)
		return
	}
	if r.URL.Path == "/oauth2/v3/certs" {
		s.handleGoogleJWKS(recorder, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/projects/") && strings.HasSuffix(r.URL.Path, "/messages:send") {
		s.handleFCM(recorder, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/projects/") && strings.Contains(r.URL.Path, "/secrets/") && strings.HasSuffix(r.URL.Path, ":access") {
		s.handleSecretManagerREST(recorder, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/projects/") && strings.Contains(r.URL.Path, "/cryptoKeys/") && (strings.HasSuffix(r.URL.Path, ":encrypt") || strings.HasSuffix(r.URL.Path, ":decrypt")) {
		s.handleKMSREST(recorder, r)
		return
	}
	if strings.Contains(r.URL.Path, "/publishers/google/models") || strings.HasPrefix(r.URL.Path, "/v1beta/models") {
		s.handleVertex(recorder, r)
		return
	}
	if r.URL.Query().Get("X-Goog-Algorithm") != "" || (r.Method == http.MethodPost && strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data")) {
		s.handleGCSSignedRequest(recorder, r)
		return
	}
	if strings.Contains(r.UserAgent(), "gcloud-golang-storage") || r.Header.Get("X-Goog-Api-Client") != "" {
		s.handleGCSXMLMedia(recorder, r)
		return
	}
	if strings.HasPrefix(r.Header.Get("X-Amz-Target"), "AmazonSQS.") {
		s.handleSQS(recorder, r)
		return
	}
	if strings.HasPrefix(r.Header.Get("X-Amz-Target"), "DynamoDB_") {
		s.handleDynamoDB(recorder, r)
		return
	}
	if r.URL.Path == "/" && r.Method == http.MethodPost && strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		s.handleSTS(recorder, r)
		return
	}
	s.handleS3(recorder, r)
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/_fcp/ui" || strings.HasPrefix(r.URL.Path, "/_fcp/ui/"):
		s.handleDashboardUI(w, r)
	case r.URL.Path == "/_fcp/dashboard":
		s.handleDashboardAPI(w, r)
	case r.URL.Path == "/_fcp/actions":
		s.handleDashboardAction(w, r)
	case r.URL.Path == "/_fcp/snapshots":
		s.handleSnapshots(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/_fcp/health":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case r.Method == http.MethodPost && r.URL.Path == "/_fcp/reset":
		if err := s.store.Reset(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Path == "/_fcp/fcm/messages":
		writeJSON(w, http.StatusOK, fcmAdminResponse(s.store.ListFCMMessages(r.URL.Query().Get("project"))))
	case r.Method == http.MethodDelete && r.URL.Path == "/_fcp/fcm/messages":
		if err := s.store.ClearFCMMessages(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Path == "/_fcp/vertex/generations":
		writeJSON(w, http.StatusOK, map[string]any{
			"generations": s.store.ListVertexGenerations(r.URL.Query().Get("project")),
			"hint":        "FCP stores generation metadata only; prompts and responses are excluded",
		})
	case r.Method == http.MethodDelete && r.URL.Path == "/_fcp/vertex/generations":
		if err := s.store.ClearVertexGenerations(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.Header().Set("x-amzn-RequestId", requestID())
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(value)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func requestID() string {
	return time.Now().UTC().Format("20060102T150405.000000000")
}
