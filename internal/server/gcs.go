package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hjyoon/fcp/internal/state"
)

type gcsBucketInput struct {
	Name         string `json:"name"`
	Location     string `json:"location"`
	StorageClass string `json:"storageClass"`
}

type gcsObjectInput struct {
	Name        string            `json:"name"`
	ContentType string            `json:"contentType"`
	Metadata    map[string]string `json:"metadata"`
}

type gcsUpload struct {
	Bucket      string
	Name        string
	ContentType string
	Metadata    map[string]string
	Body        []byte
}

func (s *Server) handleGCS(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	switch {
	case path == "/storage/v1/b":
		s.handleGCSBuckets(w, r)
	case strings.HasPrefix(path, "/upload/storage/v1/b/"):
		s.handleGCSUpload(w, r)
	case strings.HasPrefix(path, "/_fcp/gcs-upload/"):
		s.handleGCSResumableChunk(w, r)
	case strings.HasPrefix(path, "/storage/v1/b/") || strings.HasPrefix(path, "/download/storage/v1/b/"):
		s.handleGCSResource(w, r)
	default:
		gcsError(w, http.StatusNotFound, "notFound", "Not found")
	}
}

func (s *Server) handleGCSXMLMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		gcsError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
		return
	}
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		gcsError(w, http.StatusNotFound, "notFound", "Object not found")
		return
	}
	bucket, _ := url.PathUnescape(parts[0])
	name, _ := url.PathUnescape(parts[1])
	object, body, err := s.store.GCSObject(bucket, name)
	if errors.Is(err, state.ErrGCSBucketNotFound) || errors.Is(err, state.ErrGCSObjectNotFound) {
		gcsNotFound(w, "Object", name)
		return
	}
	if err != nil {
		gcsInternalError(w, err)
		return
	}
	writeGCSMedia(w, r, object, body)
}

func (s *Server) handleGCSBuckets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var input gcsBucketInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			gcsError(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		if input.Name == "" {
			gcsError(w, http.StatusBadRequest, "required", "Bucket name is required")
			return
		}
		bucket, err := s.store.CreateGCSBucket(r.URL.Query().Get("project"), input.Name, input.Location, input.StorageClass)
		if err != nil {
			gcsInternalError(w, err)
			return
		}
		writeGCSJSON(w, http.StatusOK, gcsBucketResource(r, bucket))
	case http.MethodGet:
		buckets := s.store.ListGCSBuckets(r.URL.Query().Get("project"))
		items := make([]map[string]any, 0, len(buckets))
		for _, bucket := range buckets {
			items = append(items, gcsBucketResource(r, bucket))
		}
		writeGCSJSON(w, http.StatusOK, map[string]any{"kind": "storage#buckets", "items": items})
	default:
		gcsError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
	}
}

func (s *Server) handleGCSResource(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	path = strings.TrimPrefix(path, "/storage/v1/b/")
	path = strings.TrimPrefix(path, "/download/storage/v1/b/")
	parts := strings.SplitN(path, "/o", 2)
	bucket, _ := url.PathUnescape(parts[0])
	if len(parts) == 1 {
		s.handleGCSBucket(w, r, bucket)
		return
	}
	if parts[1] == "" || parts[1] == "/" {
		s.handleGCSObjectList(w, r, bucket)
		return
	}
	objectName, _ := url.PathUnescape(strings.TrimPrefix(parts[1], "/"))
	s.handleGCSObject(w, r, bucket, objectName)
}

func (s *Server) handleGCSBucket(w http.ResponseWriter, r *http.Request, name string) {
	switch r.Method {
	case http.MethodGet:
		bucket, err := s.store.GCSBucket(name)
		if errors.Is(err, state.ErrGCSBucketNotFound) {
			gcsNotFound(w, "Bucket", name)
			return
		}
		if err != nil {
			gcsInternalError(w, err)
			return
		}
		writeGCSJSON(w, http.StatusOK, gcsBucketResource(r, bucket))
	case http.MethodDelete:
		err := s.store.DeleteGCSBucket(name)
		if errors.Is(err, state.ErrGCSBucketNotFound) {
			gcsNotFound(w, "Bucket", name)
		} else if errors.Is(err, state.ErrGCSBucketNotEmpty) {
			gcsError(w, http.StatusConflict, "conflict", "The bucket you tried to delete was not empty")
		} else if err != nil {
			gcsInternalError(w, err)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		gcsError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
	}
}

func (s *Server) handleGCSObjectList(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.Method != http.MethodGet {
		gcsError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
		return
	}
	query := r.URL.Query()
	limit := 1000
	if value, err := strconv.Atoi(query.Get("maxResults")); err == nil && value > 0 {
		limit = value
	}
	after := state.DecodePageToken(query.Get("pageToken"))
	objects, truncated, err := s.store.ListGCSObjects(bucket, query.Get("prefix"), after, limit)
	if errors.Is(err, state.ErrGCSBucketNotFound) {
		gcsNotFound(w, "Bucket", bucket)
		return
	}
	if err != nil {
		gcsInternalError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(objects))
	for _, object := range objects {
		items = append(items, gcsObjectResource(r, object))
	}
	result := map[string]any{"kind": "storage#objects", "items": items}
	if truncated && len(objects) > 0 {
		result["nextPageToken"] = state.EncodePageToken(objects[len(objects)-1].Name)
	}
	writeGCSJSON(w, http.StatusOK, result)
}

func (s *Server) handleGCSObject(w http.ResponseWriter, r *http.Request, bucket, name string) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		object, body, err := s.store.GCSObject(bucket, name)
		if errors.Is(err, state.ErrGCSBucketNotFound) || errors.Is(err, state.ErrGCSObjectNotFound) {
			gcsNotFound(w, "Object", name)
			return
		}
		if err != nil {
			gcsInternalError(w, err)
			return
		}
		if r.URL.Query().Get("alt") == "media" || strings.HasPrefix(r.URL.Path, "/download/") {
			writeGCSMedia(w, r, object, body)
			return
		}
		writeGCSJSON(w, http.StatusOK, gcsObjectResource(r, object))
	case http.MethodPatch:
		var input gcsObjectInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			gcsError(w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		object, err := s.store.PatchGCSObject(bucket, name, input.ContentType, input.Metadata)
		if errors.Is(err, state.ErrGCSObjectNotFound) || errors.Is(err, state.ErrGCSBucketNotFound) {
			gcsNotFound(w, "Object", name)
			return
		}
		if err != nil {
			gcsInternalError(w, err)
			return
		}
		writeGCSJSON(w, http.StatusOK, gcsObjectResource(r, object))
	case http.MethodDelete:
		err := s.store.DeleteGCSObject(bucket, name)
		if errors.Is(err, state.ErrGCSObjectNotFound) || errors.Is(err, state.ErrGCSBucketNotFound) {
			gcsNotFound(w, "Object", name)
		} else if err != nil {
			gcsInternalError(w, err)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		gcsError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
	}
}

func (s *Server) handleGCSUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		gcsError(w, http.StatusMethodNotAllowed, "methodNotAllowed", "Method not allowed")
		return
	}
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/upload/storage/v1/b/")
	parts := strings.SplitN(path, "/o", 2)
	bucket, _ := url.PathUnescape(parts[0])
	uploadType := r.URL.Query().Get("uploadType")
	input := gcsObjectInput{Name: r.URL.Query().Get("name"), ContentType: r.Header.Get("Content-Type")}
	var body []byte
	var err error
	switch uploadType {
	case "", "media":
		body, err = io.ReadAll(r.Body)
	case "multipart":
		input, body, err = readGCSMultipart(r)
	case "resumable":
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&input)
		}
		if input.Name == "" {
			input.Name = r.URL.Query().Get("name")
		}
		id := strings.ReplaceAll(requestID(), ".", "")
		s.gcsUploads.Store(id, &gcsUpload{Bucket: bucket, Name: input.Name, ContentType: input.ContentType, Metadata: input.Metadata})
		w.Header().Set("Location", externalBaseURL(r)+"/_fcp/gcs-upload/"+id)
		w.Header().Set("X-GUploader-UploadID", id)
		w.WriteHeader(http.StatusOK)
		return
	default:
		gcsError(w, http.StatusBadRequest, "invalid", "Unsupported uploadType")
		return
	}
	if err != nil {
		gcsError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if input.Name == "" {
		input.Name = r.URL.Query().Get("name")
	}
	if input.Name == "" {
		gcsError(w, http.StatusBadRequest, "required", "Object name is required")
		return
	}
	object, err := s.store.PutGCSObject(bucket, input.Name, body, input.ContentType, input.Metadata)
	if errors.Is(err, state.ErrGCSBucketNotFound) {
		gcsNotFound(w, "Bucket", bucket)
		return
	}
	if err != nil {
		gcsInternalError(w, err)
		return
	}
	writeGCSJSON(w, http.StatusOK, gcsObjectResource(r, object))
}

func readGCSMultipart(r *http.Request) (gcsObjectInput, []byte, error) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return gcsObjectInput{}, nil, errors.New("invalid multipart upload")
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	metadataPart, err := reader.NextPart()
	if err != nil {
		return gcsObjectInput{}, nil, err
	}
	var input gcsObjectInput
	if err := json.NewDecoder(metadataPart).Decode(&input); err != nil {
		return input, nil, err
	}
	mediaPart, err := reader.NextPart()
	if err != nil {
		return input, nil, err
	}
	body, err := io.ReadAll(mediaPart)
	if input.ContentType == "" {
		input.ContentType = mediaPart.Header.Get("Content-Type")
	}
	return input, body, err
}

func (s *Server) handleGCSResumableChunk(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/_fcp/gcs-upload/")
	value, ok := s.gcsUploads.Load(id)
	if !ok {
		gcsError(w, http.StatusNotFound, "notFound", "Upload session not found")
		return
	}
	upload := value.(*gcsUpload)
	chunk, err := io.ReadAll(r.Body)
	if err != nil {
		gcsError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	upload.Body = append(upload.Body, chunk...)
	contentRange := r.Header.Get("Content-Range")
	incomplete := strings.Contains(contentRange, "/*") || (contentRange != "" && !strings.Contains(contentRange, "/"+strconv.Itoa(len(upload.Body))))
	if incomplete {
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(upload.Body)-1))
		if r.Header.Get("X-GUploader-No-308") == "yes" {
			w.Header().Set("X-Http-Status-Code-Override", "308")
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusPermanentRedirect)
		}
		return
	}
	object, err := s.store.PutGCSObject(upload.Bucket, upload.Name, upload.Body, upload.ContentType, upload.Metadata)
	if err != nil {
		gcsInternalError(w, err)
		return
	}
	s.gcsUploads.Delete(id)
	writeGCSJSON(w, http.StatusOK, gcsObjectResource(r, object))
}

func writeGCSMedia(w http.ResponseWriter, r *http.Request, object state.GCSObject, body []byte) {
	start, end, partial := parseRange(r.Header.Get("Range"), int64(len(body)))
	if partial {
		body = body[start : end+1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, object.Size))
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Content-Type", object.ContentType)
	w.Header().Set("ETag", object.ETag)
	w.Header().Set("x-goog-generation", strconv.FormatInt(object.Generation, 10))
	w.Header().Set("x-goog-hash", "crc32c="+object.CRC32C+",md5="+object.MD5Hash)
	status := http.StatusOK
	if partial {
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func parseRange(header string, size int64) (int64, int64, bool) {
	if !strings.HasPrefix(header, "bytes=") || size == 0 {
		return 0, size - 1, false
	}
	parts := strings.SplitN(strings.TrimPrefix(header, "bytes="), "-", 2)
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, size - 1, false
	}
	end := size - 1
	if len(parts) == 2 && parts[1] != "" {
		if value, err := strconv.ParseInt(parts[1], 10, 64); err == nil && value >= start && value < size {
			end = value
		}
	}
	return start, end, true
}

func gcsBucketResource(r *http.Request, bucket state.GCSBucket) map[string]any {
	return map[string]any{"kind": "storage#bucket", "id": bucket.Name, "name": bucket.Name, "projectNumber": "000000000000", "location": bucket.Location, "storageClass": bucket.StorageClass, "timeCreated": bucket.CreatedAt.Format(time.RFC3339Nano), "updated": bucket.CreatedAt.Format(time.RFC3339Nano), "metageneration": "1", "selfLink": externalBaseURL(r) + "/storage/v1/b/" + url.PathEscape(bucket.Name)}
}
func gcsObjectResource(r *http.Request, object state.GCSObject) map[string]any {
	return map[string]any{"kind": "storage#object", "id": object.Bucket + "/" + object.Name + "/" + strconv.FormatInt(object.Generation, 10), "name": object.Name, "bucket": object.Bucket, "generation": strconv.FormatInt(object.Generation, 10), "metageneration": strconv.FormatInt(object.Metageneration, 10), "contentType": object.ContentType, "storageClass": "STANDARD", "size": strconv.FormatInt(object.Size, 10), "md5Hash": object.MD5Hash, "crc32c": object.CRC32C, "etag": object.ETag, "metadata": object.Metadata, "timeCreated": object.CreatedAt.Format(time.RFC3339Nano), "updated": object.UpdatedAt.Format(time.RFC3339Nano), "selfLink": externalBaseURL(r) + "/storage/v1/b/" + url.PathEscape(object.Bucket) + "/o/" + url.PathEscape(object.Name), "mediaLink": externalBaseURL(r) + "/download/storage/v1/b/" + url.PathEscape(object.Bucket) + "/o/" + url.PathEscape(object.Name) + "?alt=media"}
}

func externalBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if value := r.Header.Get("X-Forwarded-Proto"); value != "" {
		scheme = strings.Split(value, ",")[0]
	}
	return scheme + "://" + r.Host
}
func writeGCSJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("x-guploader-uploadid", requestID())
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func gcsNotFound(w http.ResponseWriter, kind, name string) {
	gcsError(w, http.StatusNotFound, "notFound", fmt.Sprintf("%s %s not found", kind, name))
}
func gcsInternalError(w http.ResponseWriter, err error) {
	gcsError(w, http.StatusInternalServerError, "internalError", err.Error())
}
func gcsError(w http.ResponseWriter, status int, reason, message string) {
	writeGCSJSON(w, status, map[string]any{"error": map[string]any{"code": status, "message": message, "errors": []map[string]string{{"domain": "global", "reason": reason, "message": message}}}})
}
