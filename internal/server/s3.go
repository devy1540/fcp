package server

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hjyoon/fcp/internal/state"
)

const s3Namespace = "http://s3.amazonaws.com/doc/2006-03-01/"

type listAllBucketsResult struct {
	XMLName xml.Name   `xml:"ListAllMyBucketsResult"`
	XMLNS   string     `xml:"xmlns,attr"`
	Owner   s3Owner    `xml:"Owner"`
	Buckets []s3Bucket `xml:"Buckets>Bucket"`
}

type s3Owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}
type s3Bucket struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
}

type listObjectsResult struct {
	XMLName               xml.Name   `xml:"ListBucketResult"`
	XMLNS                 string     `xml:"xmlns,attr"`
	Name                  string     `xml:"Name"`
	Prefix                string     `xml:"Prefix"`
	KeyCount              int        `xml:"KeyCount"`
	MaxKeys               int        `xml:"MaxKeys"`
	IsTruncated           bool       `xml:"IsTruncated"`
	ContinuationToken     string     `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string     `xml:"NextContinuationToken,omitempty"`
	Contents              []s3Object `xml:"Contents"`
}

type s3Object struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

type copyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	XMLNS        string   `xml:"xmlns,attr"`
	ETag         string   `xml:"ETag"`
	LastModified string   `xml:"LastModified"`
}

type initiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	XMLNS    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

type completeMultipartUploadRequest struct {
	Parts []completeMultipartPart `xml:"Part"`
}

type completeMultipartPart struct {
	ETag       string `xml:"ETag"`
	PartNumber int    `xml:"PartNumber"`
}

type completeMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	XMLNS    string   `xml:"xmlns,attr"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

type listPartsResult struct {
	XMLName              xml.Name           `xml:"ListPartsResult"`
	XMLNS                string             `xml:"xmlns,attr"`
	Bucket               string             `xml:"Bucket"`
	Key                  string             `xml:"Key"`
	UploadID             string             `xml:"UploadId"`
	PartNumberMarker     int                `xml:"PartNumberMarker"`
	NextPartNumberMarker int                `xml:"NextPartNumberMarker,omitempty"`
	MaxParts             int                `xml:"MaxParts"`
	IsTruncated          bool               `xml:"IsTruncated"`
	Parts                []multipartPartXML `xml:"Part"`
}

type multipartPartXML struct {
	PartNumber   int    `xml:"PartNumber"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

type listMultipartUploadsResult struct {
	XMLName     xml.Name             `xml:"ListMultipartUploadsResult"`
	XMLNS       string               `xml:"xmlns,attr"`
	Bucket      string               `xml:"Bucket"`
	Prefix      string               `xml:"Prefix"`
	MaxUploads  int                  `xml:"MaxUploads"`
	IsTruncated bool                 `xml:"IsTruncated"`
	Uploads     []multipartUploadXML `xml:"Upload"`
}

type multipartUploadXML struct {
	Key          string `xml:"Key"`
	UploadID     string `xml:"UploadId"`
	Initiated    string `xml:"Initiated"`
	StorageClass string `xml:"StorageClass"`
}

type notificationConfiguration struct {
	XMLName xml.Name             `xml:"NotificationConfiguration"`
	XMLNS   string               `xml:"xmlns,attr,omitempty"`
	Queues  []queueConfiguration `xml:"QueueConfiguration"`
}

type queueConfiguration struct {
	ID       string             `xml:"Id"`
	QueueARN string             `xml:"Queue"`
	Events   []string           `xml:"Event"`
	Filter   notificationFilter `xml:"Filter"`
}

type notificationFilter struct {
	Rules []filterRule `xml:"S3Key>FilterRule"`
}
type filterRule struct {
	Name  string `xml:"Name"`
	Value string `xml:"Value"`
}

func (s *Server) handleS3(w http.ResponseWriter, r *http.Request) {
	bucket, key := s3Resource(r)
	query := r.URL.Query()
	if bucket == "" {
		if r.Method == http.MethodGet {
			s.listBuckets(w)
			return
		}
		s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed", r.URL.Path)
		return
	}

	if _, ok := query["notification"]; ok {
		s.handleBucketNotification(w, r, bucket)
		return
	}

	if key == "" {
		if _, ok := query["uploads"]; ok {
			if r.Method == http.MethodGet {
				s.listMultipartUploads(w, r, bucket)
			} else {
				s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed", bucket)
			}
			return
		}
		switch r.Method {
		case http.MethodPut:
			if err := s.store.CreateBucket(bucket); err != nil {
				s3InternalError(w, err)
				return
			}
			w.Header().Set("Location", "/"+bucket)
			w.WriteHeader(http.StatusOK)
		case http.MethodHead:
			if !s.store.HasBucket(bucket) {
				s3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist", bucket)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			err := s.store.DeleteBucket(bucket)
			switch {
			case errors.Is(err, state.ErrBucketNotFound):
				s3Error(w, http.StatusNotFound, "NoSuchBucket", err.Error(), bucket)
			case errors.Is(err, state.ErrBucketNotEmpty):
				s3Error(w, http.StatusConflict, "BucketNotEmpty", err.Error(), bucket)
			case err != nil:
				s3InternalError(w, err)
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		case http.MethodGet:
			s.listObjects(w, r, bucket)
		default:
			s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed", bucket)
		}
		return
	}

	if _, ok := query["uploads"]; ok {
		if r.Method == http.MethodPost {
			s.createMultipartUpload(w, r, bucket, key)
		} else {
			s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed", key)
		}
		return
	}
	if uploadID := query.Get("uploadId"); uploadID != "" {
		switch r.Method {
		case http.MethodPut:
			s.uploadMultipartPart(w, r, bucket, key, uploadID)
		case http.MethodGet:
			s.listMultipartParts(w, r, bucket, key, uploadID)
		case http.MethodPost:
			s.completeMultipartUpload(w, r, bucket, key, uploadID)
		case http.MethodDelete:
			s.abortMultipartUpload(w, bucket, key, uploadID)
		default:
			s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed", key)
		}
		return
	}

	switch r.Method {
	case http.MethodPut:
		if r.Header.Get("x-amz-copy-source") != "" {
			s.copyObject(w, r, bucket, key)
		} else {
			s.putObject(w, r, bucket, key)
		}
	case http.MethodGet, http.MethodHead:
		s.getObject(w, r, bucket, key)
	case http.MethodDelete:
		if err := s.store.DeleteObject(bucket, key); errors.Is(err, state.ErrBucketNotFound) {
			s3Error(w, http.StatusNotFound, "NoSuchBucket", err.Error(), bucket)
		} else if err != nil {
			s3InternalError(w, err)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed", key)
	}
}

func s3Resource(r *http.Request) (string, string) {
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	parts := strings.SplitN(path, "/", 2)
	bucket := ""
	key := ""
	host := strings.Split(r.Host, ":")[0]
	if strings.HasSuffix(host, ".localhost") {
		bucket = strings.TrimSuffix(host, ".localhost")
		if path != "" {
			key, _ = url.PathUnescape(path)
		}
		return bucket, key
	}
	if len(parts) > 0 {
		bucket, _ = url.PathUnescape(parts[0])
	}
	if len(parts) == 2 {
		key, _ = url.PathUnescape(parts[1])
	}
	return bucket, key
}

func (s *Server) listBuckets(w http.ResponseWriter) {
	result := listAllBucketsResult{XMLNS: s3Namespace, Owner: s3Owner{ID: state.AccountID(), DisplayName: "fcp"}}
	for _, b := range s.store.ListBuckets() {
		result.Buckets = append(result.Buckets, s3Bucket{Name: b.Name, CreationDate: b.CreatedAt.Format(time.RFC3339)})
	}
	writeXML(w, http.StatusOK, result)
}

func (s *Server) putObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s3Error(w, http.StatusBadRequest, "InvalidRequest", err.Error(), key)
		return
	}
	metadata := s3Metadata(r.Header)
	obj, err := s.store.PutObject(bucket, key, body, r.Header.Get("Content-Type"), metadata)
	if errors.Is(err, state.ErrBucketNotFound) {
		s3Error(w, http.StatusNotFound, "NoSuchBucket", err.Error(), bucket)
		return
	}
	if err != nil {
		s3InternalError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", obj.ETag))
	w.Header().Set("x-amz-request-id", requestID())
	w.WriteHeader(http.StatusOK)
}

func (s *Server) createMultipartUpload(w http.ResponseWriter, r *http.Request, bucket, key string) {
	upload, err := s.store.CreateMultipartUpload(bucket, key, r.Header.Get("Content-Type"), s3Metadata(r.Header))
	if errors.Is(err, state.ErrBucketNotFound) {
		s3Error(w, http.StatusNotFound, "NoSuchBucket", err.Error(), bucket)
		return
	}
	if err != nil {
		s3InternalError(w, err)
		return
	}
	writeXML(w, http.StatusOK, initiateMultipartUploadResult{
		XMLNS: s3Namespace, Bucket: bucket, Key: key, UploadID: upload.ID,
	})
}

func (s *Server) uploadMultipartPart(w http.ResponseWriter, r *http.Request, bucket, key, uploadID string) {
	partNumber, err := strconv.Atoi(r.URL.Query().Get("partNumber"))
	if err != nil || partNumber < 1 || partNumber > 10_000 {
		s3Error(w, http.StatusBadRequest, "InvalidArgument", "Part number must be an integer between 1 and 10000", key)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s3Error(w, http.StatusBadRequest, "InvalidRequest", err.Error(), key)
		return
	}
	part, err := s.store.UploadMultipartPart(bucket, key, uploadID, partNumber, body)
	if errors.Is(err, state.ErrMultipartUploadNotFound) {
		s3Error(w, http.StatusNotFound, "NoSuchUpload", err.Error(), uploadID)
		return
	}
	if errors.Is(err, state.ErrInvalidPart) {
		s3Error(w, http.StatusBadRequest, "InvalidArgument", err.Error(), key)
		return
	}
	if err != nil {
		s3InternalError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", part.ETag))
	w.Header().Set("x-amz-request-id", requestID())
	w.WriteHeader(http.StatusOK)
}

func (s *Server) listMultipartParts(w http.ResponseWriter, r *http.Request, bucket, key, uploadID string) {
	marker := 0
	if value := r.URL.Query().Get("part-number-marker"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			s3Error(w, http.StatusBadRequest, "InvalidArgument", "Part number marker must be a non-negative integer", key)
			return
		}
		marker = parsed
	}
	maxParts := 1000
	if value := r.URL.Query().Get("max-parts"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 1000 {
			s3Error(w, http.StatusBadRequest, "InvalidArgument", "Max parts must be between 0 and 1000", key)
			return
		}
		maxParts = parsed
	}
	_, allParts, err := s.store.ListMultipartParts(bucket, key, uploadID)
	if errors.Is(err, state.ErrMultipartUploadNotFound) {
		s3Error(w, http.StatusNotFound, "NoSuchUpload", err.Error(), uploadID)
		return
	}
	if err != nil {
		s3InternalError(w, err)
		return
	}
	parts := make([]state.MultipartPart, 0, len(allParts))
	for _, part := range allParts {
		if part.PartNumber > marker {
			parts = append(parts, part)
		}
	}
	truncated := len(parts) > maxParts
	if truncated {
		parts = parts[:maxParts]
	}
	result := listPartsResult{
		XMLNS: s3Namespace, Bucket: bucket, Key: key, UploadID: uploadID,
		PartNumberMarker: marker, MaxParts: maxParts, IsTruncated: truncated,
	}
	for _, part := range parts {
		result.Parts = append(result.Parts, multipartPartXML{
			PartNumber: part.PartNumber, LastModified: part.LastModified.Format(time.RFC3339),
			ETag: fmt.Sprintf("\"%s\"", part.ETag), Size: part.Size,
		})
	}
	if truncated && len(parts) > 0 {
		result.NextPartNumberMarker = parts[len(parts)-1].PartNumber
	}
	writeXML(w, http.StatusOK, result)
}

func (s *Server) completeMultipartUpload(w http.ResponseWriter, r *http.Request, bucket, key, uploadID string) {
	var request completeMultipartUploadRequest
	if err := xml.NewDecoder(r.Body).Decode(&request); err != nil {
		s3Error(w, http.StatusBadRequest, "MalformedXML", err.Error(), key)
		return
	}
	completed := make([]state.CompletedMultipartPart, 0, len(request.Parts))
	for _, part := range request.Parts {
		completed = append(completed, state.CompletedMultipartPart{PartNumber: part.PartNumber, ETag: part.ETag})
	}
	obj, err := s.store.CompleteMultipartUpload(bucket, key, uploadID, completed)
	switch {
	case errors.Is(err, state.ErrMultipartUploadNotFound):
		s3Error(w, http.StatusNotFound, "NoSuchUpload", err.Error(), uploadID)
		return
	case errors.Is(err, state.ErrInvalidPart):
		s3Error(w, http.StatusBadRequest, "InvalidPart", err.Error(), key)
		return
	case errors.Is(err, state.ErrInvalidPartOrder):
		s3Error(w, http.StatusBadRequest, "InvalidPartOrder", err.Error(), key)
		return
	case errors.Is(err, state.ErrEntityTooSmall):
		s3Error(w, http.StatusBadRequest, "EntityTooSmall", err.Error(), key)
		return
	case err != nil:
		s3InternalError(w, err)
		return
	}
	writeXML(w, http.StatusOK, completeMultipartUploadResult{
		XMLNS: s3Namespace, Location: r.URL.Path, Bucket: bucket, Key: key, ETag: fmt.Sprintf("\"%s\"", obj.ETag),
	})
}

func (s *Server) abortMultipartUpload(w http.ResponseWriter, bucket, key, uploadID string) {
	err := s.store.AbortMultipartUpload(bucket, key, uploadID)
	if errors.Is(err, state.ErrMultipartUploadNotFound) {
		s3Error(w, http.StatusNotFound, "NoSuchUpload", err.Error(), uploadID)
		return
	}
	if err != nil {
		s3InternalError(w, err)
		return
	}
	w.Header().Set("x-amz-request-id", requestID())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMultipartUploads(w http.ResponseWriter, r *http.Request, bucket string) {
	maxUploads := 1000
	if value := r.URL.Query().Get("max-uploads"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 1000 {
			s3Error(w, http.StatusBadRequest, "InvalidArgument", "Max uploads must be between 0 and 1000", bucket)
			return
		}
		maxUploads = parsed
	}
	uploads, err := s.store.ListMultipartUploads(bucket, r.URL.Query().Get("prefix"))
	if errors.Is(err, state.ErrBucketNotFound) {
		s3Error(w, http.StatusNotFound, "NoSuchBucket", err.Error(), bucket)
		return
	}
	if err != nil {
		s3InternalError(w, err)
		return
	}
	truncated := len(uploads) > maxUploads
	if truncated {
		uploads = uploads[:maxUploads]
	}
	result := listMultipartUploadsResult{
		XMLNS: s3Namespace, Bucket: bucket, Prefix: r.URL.Query().Get("prefix"),
		MaxUploads: maxUploads, IsTruncated: truncated,
	}
	for _, upload := range uploads {
		result.Uploads = append(result.Uploads, multipartUploadXML{
			Key: upload.Key, UploadID: upload.ID, Initiated: upload.CreatedAt.Format(time.RFC3339), StorageClass: "STANDARD",
		})
	}
	writeXML(w, http.StatusOK, result)
}

func (s *Server) copyObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	sourceBucket, sourceKey, err := parseCopySource(r.Header.Get("x-amz-copy-source"))
	if err != nil {
		s3Error(w, http.StatusBadRequest, "InvalidArgument", err.Error(), key)
		return
	}
	source, body, err := s.store.GetObject(sourceBucket, sourceKey)
	if errors.Is(err, state.ErrBucketNotFound) {
		s3Error(w, http.StatusNotFound, "NoSuchBucket", err.Error(), sourceBucket)
		return
	}
	if errors.Is(err, state.ErrObjectNotFound) {
		s3Error(w, http.StatusNotFound, "NoSuchKey", err.Error(), sourceKey)
		return
	}
	if err != nil {
		s3InternalError(w, err)
		return
	}
	contentType := source.ContentType
	metadata := source.Metadata
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("x-amz-metadata-directive")), "REPLACE") {
		contentType = r.Header.Get("Content-Type")
		metadata = s3Metadata(r.Header)
	}
	copied, err := s.store.PutObject(bucket, key, body, contentType, metadata)
	if errors.Is(err, state.ErrBucketNotFound) {
		s3Error(w, http.StatusNotFound, "NoSuchBucket", err.Error(), bucket)
		return
	}
	if err != nil {
		s3InternalError(w, err)
		return
	}
	w.Header().Set("x-amz-request-id", requestID())
	writeXML(w, http.StatusOK, copyObjectResult{
		XMLNS: s3Namespace, ETag: fmt.Sprintf("\"%s\"", copied.ETag), LastModified: copied.LastModified.Format(time.RFC3339),
	})
}

func parseCopySource(value string) (string, string, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "/")
	value = strings.SplitN(value, "?", 2)[0]
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return "", "", fmt.Errorf("invalid copy source: %w", err)
	}
	parts := strings.SplitN(decoded, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("copy source must contain a bucket and key")
	}
	return parts[0], parts[1], nil
}

func s3Metadata(header http.Header) map[string]string {
	metadata := map[string]string{}
	for name, values := range header {
		if strings.HasPrefix(strings.ToLower(name), "x-amz-meta-") {
			metadata[strings.TrimPrefix(strings.ToLower(name), "x-amz-meta-")] = strings.Join(values, ",")
		}
	}
	return metadata
}

func (s *Server) getObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	obj, body, err := s.store.GetObject(bucket, key)
	if errors.Is(err, state.ErrBucketNotFound) {
		s3Error(w, http.StatusNotFound, "NoSuchBucket", err.Error(), bucket)
		return
	}
	if errors.Is(err, state.ErrObjectNotFound) {
		s3Error(w, http.StatusNotFound, "NoSuchKey", err.Error(), key)
		return
	}
	if err != nil {
		s3InternalError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", obj.ETag))
	w.Header().Set("Last-Modified", obj.LastModified.Format(http.TimeFormat))
	if obj.ContentType != "" {
		w.Header().Set("Content-Type", obj.ContentType)
	}
	for name, value := range obj.Metadata {
		w.Header().Set("x-amz-meta-"+name, value)
	}
	http.ServeContent(w, r, key, obj.LastModified, bytes.NewReader(body))
}

func (s *Server) listObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	query := r.URL.Query()
	maxKeys := 1000
	if n, err := strconv.Atoi(query.Get("max-keys")); err == nil && n >= 0 && n <= 1000 {
		maxKeys = n
	}
	after := ""
	if token := query.Get("continuation-token"); token != "" {
		if raw, err := base64.RawURLEncoding.DecodeString(token); err == nil {
			after = string(raw)
		}
	}
	if query.Get("start-after") > after {
		after = query.Get("start-after")
	}
	objects, truncated, err := s.store.ListObjects(bucket, query.Get("prefix"), after, maxKeys)
	if errors.Is(err, state.ErrBucketNotFound) {
		s3Error(w, http.StatusNotFound, "NoSuchBucket", err.Error(), bucket)
		return
	}
	if err != nil {
		s3InternalError(w, err)
		return
	}
	result := listObjectsResult{XMLNS: s3Namespace, Name: bucket, Prefix: query.Get("prefix"), KeyCount: len(objects), MaxKeys: maxKeys, IsTruncated: truncated, ContinuationToken: query.Get("continuation-token")}
	for _, obj := range objects {
		result.Contents = append(result.Contents, s3Object{Key: obj.Key, LastModified: obj.LastModified.Format(time.RFC3339), ETag: fmt.Sprintf("\"%s\"", obj.ETag), Size: obj.Size, StorageClass: "STANDARD"})
	}
	if truncated && len(objects) > 0 {
		result.NextContinuationToken = base64.RawURLEncoding.EncodeToString([]byte(objects[len(objects)-1].Key))
	}
	writeXML(w, http.StatusOK, result)
}

func (s *Server) handleBucketNotification(w http.ResponseWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodPut:
		var config notificationConfiguration
		if err := xml.NewDecoder(r.Body).Decode(&config); err != nil {
			s3Error(w, http.StatusBadRequest, "MalformedXML", err.Error(), bucket)
			return
		}
		notifications := make([]state.Notification, 0, len(config.Queues))
		for _, q := range config.Queues {
			n := state.Notification{ID: q.ID, QueueARN: q.QueueARN, Events: q.Events}
			if n.ID == "" {
				n.ID = requestID()
			}
			for _, rule := range q.Filter.Rules {
				if rule.Name == "prefix" {
					n.Prefix = rule.Value
				}
				if rule.Name == "suffix" {
					n.Suffix = rule.Value
				}
			}
			notifications = append(notifications, n)
		}
		err := s.store.SetNotifications(bucket, notifications)
		if errors.Is(err, state.ErrBucketNotFound) {
			s3Error(w, http.StatusNotFound, "NoSuchBucket", err.Error(), bucket)
			return
		}
		if errors.Is(err, state.ErrQueueNotFound) {
			s3Error(w, http.StatusBadRequest, "InvalidArgument", "Unable to validate the following destination configurations", bucket)
			return
		}
		if err != nil {
			s3InternalError(w, err)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		notifications, err := s.store.Notifications(bucket)
		if errors.Is(err, state.ErrBucketNotFound) {
			s3Error(w, http.StatusNotFound, "NoSuchBucket", err.Error(), bucket)
			return
		}
		if err != nil {
			s3InternalError(w, err)
			return
		}
		config := notificationConfiguration{XMLNS: s3Namespace}
		for _, n := range notifications {
			q := queueConfiguration{ID: n.ID, QueueARN: n.QueueARN, Events: n.Events}
			if n.Prefix != "" {
				q.Filter.Rules = append(q.Filter.Rules, filterRule{Name: "prefix", Value: n.Prefix})
			}
			if n.Suffix != "" {
				q.Filter.Rules = append(q.Filter.Rules, filterRule{Name: "suffix", Value: n.Suffix})
			}
			config.Queues = append(config.Queues, q)
		}
		writeXML(w, http.StatusOK, config)
	default:
		s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed", bucket)
	}
}

func writeXML(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("x-amz-request-id", requestID())
	w.WriteHeader(status)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(value)
}

type s3ErrorResponse struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource"`
	RequestID string   `xml:"RequestId"`
}

func s3Error(w http.ResponseWriter, status int, code, message, resource string) {
	writeXML(w, status, s3ErrorResponse{Code: code, Message: message, Resource: resource, RequestID: requestID()})
}
func s3InternalError(w http.ResponseWriter, err error) {
	s3Error(w, http.StatusInternalServerError, "InternalError", err.Error(), "")
}
