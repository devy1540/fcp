package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/hjyoon/fcp/internal/state"
)

type dashboardActionRequest struct {
	Operation  string            `json:"operation"`
	Service    string            `json:"service,omitempty"`
	Resource   string            `json:"resource,omitempty"`
	Kind       string            `json:"kind,omitempty"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

type dashboardRequestError struct {
	status  int
	message string
}

func (e *dashboardRequestError) Error() string { return e.message }

var (
	s3DashboardBucketName    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	gcsDashboardBucketName   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$`)
	sqsDashboardQueueName    = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	dynamoDashboardTableName = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,255}$`)
	dynamoDashboardKeyName   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,255}$`)
	pubSubDashboardID        = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._~+%-]{2,254}$`)
	locationDashboardName    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

func (s *Server) handleDashboardAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		writeDashboardActionError(w, http.StatusUnsupportedMediaType, "application/json request body is required")
		return
	}
	var request dashboardActionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeDashboardActionError(w, http.StatusBadRequest, "invalid dashboard action")
		return
	}

	var err error
	message := ""
	switch request.Operation {
	case "reset-workload":
		err = s.store.ResetWorkloadData()
		message = "테스트 데이터를 비웠습니다."
	case "purge":
		switch request.Service {
		case "sqs":
			err = s.store.PurgeQueue(request.Resource)
			message = "SQS 메시지를 비웠습니다."
		case "pubsub":
			err = s.store.PurgePubSubSubscription(request.Resource)
			message = "Pub/Sub 메시지를 비웠습니다."
		case "fcm":
			err = s.store.ClearFCMMessages()
			message = "FCM 캡처를 비웠습니다."
		case "vertex":
			err = s.store.ClearVertexGenerations()
			message = "Vertex AI 호출 기록을 비웠습니다."
		case "dynamodb":
			err = s.store.ClearDynamoTable(request.Resource)
			message = "DynamoDB 테이블 아이템을 비웠습니다."
		default:
			writeDashboardActionError(w, http.StatusBadRequest, "unsupported purge service")
			return
		}
	case "create":
		message, err = s.createDashboardResource(request)
	case "delete":
		message, err = s.deleteDashboardResource(request)
	default:
		writeDashboardActionError(w, http.StatusBadRequest, "unsupported dashboard action")
		return
	}
	var requestError *dashboardRequestError
	if errors.As(err, &requestError) {
		writeDashboardActionError(w, requestError.status, requestError.message)
		return
	}
	if errors.Is(err, state.ErrQueueNotFound) || errors.Is(err, state.ErrPubSubSubscriptionNotFound) {
		writeDashboardActionError(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, state.ErrBucketNotFound) || errors.Is(err, state.ErrGCSBucketNotFound) || errors.Is(err, state.ErrPubSubTopicNotFound) {
		writeDashboardActionError(w, http.StatusNotFound, "리소스를 찾을 수 없습니다.")
		return
	}
	if errors.Is(err, state.ErrDynamoTableNotFound) {
		writeDashboardActionError(w, http.StatusNotFound, "DynamoDB 테이블을 찾을 수 없습니다.")
		return
	}
	if errors.Is(err, state.ErrBucketNotEmpty) || errors.Is(err, state.ErrGCSBucketNotEmpty) {
		writeDashboardActionError(w, http.StatusConflict, "버킷이 비어 있지 않습니다. 객체를 먼저 삭제하세요.")
		return
	}
	if errors.Is(err, state.ErrInvalidQueueAttribute) {
		writeDashboardActionError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeDashboardActionError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": message})
}

func (s *Server) createDashboardResource(request dashboardActionRequest) (string, error) {
	resource := strings.TrimSpace(request.Resource)
	kind := strings.ToLower(strings.TrimSpace(request.Kind))
	switch request.Service {
	case "s3":
		if kind != "" && kind != "bucket" {
			return "", dashboardBadRequest("S3에서는 버킷만 만들 수 있습니다.")
		}
		if !validS3DashboardBucket(resource) {
			return "", dashboardBadRequest("S3 버킷 이름은 소문자·숫자·점·하이픈으로 구성된 3~63자여야 합니다.")
		}
		if s.store.HasBucket(resource) {
			return "", dashboardConflict("같은 이름의 S3 버킷이 이미 있습니다.")
		}
		if err := s.store.CreateBucket(resource); err != nil {
			return "", err
		}
		return fmt.Sprintf("S3 버킷 %s을(를) 만들었습니다.", resource), nil
	case "sqs":
		if kind != "" && kind != "queue" {
			return "", dashboardBadRequest("SQS에서는 큐만 만들 수 있습니다.")
		}
		queueType := strings.ToLower(strings.TrimSpace(request.Parameters["queueType"]))
		if queueType == "" {
			queueType = "standard"
		}
		if queueType != "standard" && queueType != "fifo" {
			return "", dashboardBadRequest("큐 유형은 Standard 또는 FIFO여야 합니다.")
		}
		if !validSQSDashboardQueue(resource, queueType == "fifo") {
			return "", dashboardBadRequest("SQS 큐 이름 형식이 올바르지 않습니다. FIFO 큐는 .fifo로 끝나야 합니다.")
		}
		if _, err := s.store.Queue(resource); err == nil {
			return "", dashboardConflict("같은 이름의 SQS 큐가 이미 있습니다.")
		} else if !errors.Is(err, state.ErrQueueNotFound) {
			return "", err
		}
		contentDedup, err := dashboardBool(request.Parameters["contentBasedDeduplication"])
		if err != nil {
			return "", dashboardBadRequest("Content dedup 값이 올바르지 않습니다.")
		}
		if queueType != "fifo" && contentDedup {
			return "", dashboardBadRequest("Content dedup은 FIFO 큐에서만 사용할 수 있습니다.")
		}
		attributes := map[string]string{}
		if queueType == "fifo" {
			attributes["FifoQueue"] = "true"
			attributes["ContentBasedDeduplication"] = strconv.FormatBool(contentDedup)
		}
		if _, err := s.store.CreateQueue(resource, attributes); err != nil {
			return "", err
		}
		return fmt.Sprintf("SQS 큐 %s을(를) 만들었습니다.", resource), nil
	case "gcs":
		if kind != "" && kind != "bucket" {
			return "", dashboardBadRequest("Cloud Storage에서는 버킷만 만들 수 있습니다.")
		}
		if !validGCSDashboardBucket(resource) {
			return "", dashboardBadRequest("Cloud Storage 버킷 이름은 소문자·숫자·점·하이픈·밑줄로 구성된 3~63자여야 합니다.")
		}
		if _, err := s.store.GCSBucket(resource); err == nil {
			return "", dashboardConflict("같은 이름의 Cloud Storage 버킷이 이미 있습니다.")
		} else if !errors.Is(err, state.ErrGCSBucketNotFound) {
			return "", err
		}
		location := strings.TrimSpace(request.Parameters["location"])
		if location == "" {
			location = "ASIA-NORTHEAST3"
		}
		if !locationDashboardName.MatchString(location) {
			return "", dashboardBadRequest("리전 이름 형식이 올바르지 않습니다.")
		}
		storageClass := strings.ToUpper(strings.TrimSpace(request.Parameters["storageClass"]))
		if storageClass == "" {
			storageClass = "STANDARD"
		}
		if storageClass != "STANDARD" && storageClass != "NEARLINE" && storageClass != "COLDLINE" && storageClass != "ARCHIVE" {
			return "", dashboardBadRequest("지원하지 않는 Storage class입니다.")
		}
		if _, err := s.store.CreateGCSBucket(s.projectID, resource, location, storageClass); err != nil {
			return "", err
		}
		return fmt.Sprintf("Cloud Storage 버킷 %s을(를) 만들었습니다.", resource), nil
	case "dynamodb":
		if kind != "" && kind != "table" {
			return "", dashboardBadRequest("DynamoDB에서는 테이블만 만들 수 있습니다.")
		}
		if !dynamoDashboardTableName.MatchString(resource) {
			return "", dashboardBadRequest("DynamoDB 테이블 이름은 문자·숫자·점·하이픈·밑줄로 구성된 3~255자여야 합니다.")
		}
		partitionKey := strings.TrimSpace(request.Parameters["partitionKey"])
		if partitionKey == "" {
			partitionKey = "pk"
		}
		sortKey := strings.TrimSpace(request.Parameters["sortKey"])
		if !dynamoDashboardKeyName.MatchString(partitionKey) || (sortKey != "" && !dynamoDashboardKeyName.MatchString(sortKey)) || partitionKey == sortKey {
			return "", dashboardBadRequest("Partition/Sort key 이름이 올바르지 않습니다.")
		}
		keySchema := []state.DynamoKeySchemaElement{{AttributeName: partitionKey, KeyType: "HASH"}}
		definitions := []state.DynamoAttributeDefinition{{AttributeName: partitionKey, AttributeType: "S"}}
		if sortKey != "" {
			keySchema = append(keySchema, state.DynamoKeySchemaElement{AttributeName: sortKey, KeyType: "RANGE"})
			definitions = append(definitions, state.DynamoAttributeDefinition{AttributeName: sortKey, AttributeType: "S"})
		}
		if _, err := s.store.CreateDynamoTable(resource, keySchema, definitions, "PAY_PER_REQUEST"); err != nil {
			if errors.Is(err, state.ErrDynamoTableExists) {
				return "", dashboardConflict("같은 이름의 DynamoDB 테이블이 이미 있습니다.")
			}
			return "", err
		}
		return fmt.Sprintf("DynamoDB 테이블 %s을(를) 만들었습니다.", resource), nil
	case "pubsub":
		if kind != "topic" && kind != "subscription" {
			return "", dashboardBadRequest("Pub/Sub 리소스 종류를 선택하세요.")
		}
		name, err := s.pubSubDashboardResourceName(kind, resource)
		if err != nil {
			return "", err
		}
		if kind == "topic" {
			if _, err := s.store.PubSubTopic(name); err == nil {
				return "", dashboardConflict("같은 이름의 Pub/Sub 토픽이 이미 있습니다.")
			} else if !errors.Is(err, state.ErrPubSubTopicNotFound) {
				return "", err
			}
			if _, err := s.store.CreatePubSubTopic(name, nil); err != nil {
				return "", err
			}
			return fmt.Sprintf("Pub/Sub 토픽 %s을(를) 만들었습니다.", resource), nil
		}
		if _, err := s.store.PubSubSubscription(name); err == nil {
			return "", dashboardConflict("같은 이름의 Pub/Sub 구독이 이미 있습니다.")
		} else if !errors.Is(err, state.ErrPubSubSubscriptionNotFound) {
			return "", err
		}
		topic := strings.TrimSpace(request.Parameters["topic"])
		if !strings.HasPrefix(topic, "projects/"+s.projectID+"/topics/") {
			return "", dashboardBadRequest("현재 프로젝트의 토픽을 선택하세요.")
		}
		deadline := int64(10)
		if value := strings.TrimSpace(request.Parameters["ackDeadlineSeconds"]); value != "" {
			deadline, err = strconv.ParseInt(value, 10, 32)
			if err != nil || deadline < 10 || deadline > 600 {
				return "", dashboardBadRequest("Ack deadline은 10~600초여야 합니다.")
			}
		}
		ordering, err := dashboardBool(request.Parameters["enableOrdering"])
		if err != nil {
			return "", dashboardBadRequest("Ordering 값이 올바르지 않습니다.")
		}
		if _, err := s.store.CreatePubSubSubscription(name, topic, int32(deadline), nil, ordering); err != nil {
			return "", err
		}
		return fmt.Sprintf("Pub/Sub 구독 %s을(를) 만들었습니다.", resource), nil
	default:
		return "", dashboardBadRequest("지원하지 않는 생성 서비스입니다.")
	}
}

func (s *Server) deleteDashboardResource(request dashboardActionRequest) (string, error) {
	resource := strings.TrimSpace(request.Resource)
	kind := strings.ToLower(strings.TrimSpace(request.Kind))
	if resource == "" {
		return "", dashboardBadRequest("삭제할 리소스가 필요합니다.")
	}
	switch request.Service {
	case "s3":
		if kind != "" && kind != "bucket" {
			return "", dashboardBadRequest("S3 버킷만 삭제할 수 있습니다.")
		}
		if err := s.store.DeleteBucket(resource); err != nil {
			return "", err
		}
		return fmt.Sprintf("S3 버킷 %s을(를) 삭제했습니다.", resource), nil
	case "sqs":
		if kind != "" && kind != "queue" {
			return "", dashboardBadRequest("SQS 큐만 삭제할 수 있습니다.")
		}
		if err := s.store.DeleteQueue(resource); err != nil {
			return "", err
		}
		return fmt.Sprintf("SQS 큐 %s을(를) 삭제했습니다.", resource), nil
	case "gcs":
		if kind != "" && kind != "bucket" {
			return "", dashboardBadRequest("Cloud Storage 버킷만 삭제할 수 있습니다.")
		}
		if err := s.store.DeleteGCSBucket(resource); err != nil {
			return "", err
		}
		return fmt.Sprintf("Cloud Storage 버킷 %s을(를) 삭제했습니다.", resource), nil
	case "dynamodb":
		if kind != "" && kind != "table" {
			return "", dashboardBadRequest("DynamoDB 테이블만 삭제할 수 있습니다.")
		}
		if err := s.store.DeleteDynamoTable(resource); err != nil {
			return "", err
		}
		return fmt.Sprintf("DynamoDB 테이블 %s을(를) 삭제했습니다.", resource), nil
	case "pubsub":
		switch kind {
		case "topic":
			if !strings.HasPrefix(resource, "projects/"+s.projectID+"/topics/") {
				return "", dashboardBadRequest("현재 프로젝트의 Pub/Sub 토픽만 삭제할 수 있습니다.")
			}
			if err := s.store.DeletePubSubTopic(resource); err != nil {
				return "", err
			}
			return fmt.Sprintf("Pub/Sub 토픽 %s을(를) 삭제했습니다.", shortDashboardResourceName(resource)), nil
		case "subscription":
			if !strings.HasPrefix(resource, "projects/"+s.projectID+"/subscriptions/") {
				return "", dashboardBadRequest("현재 프로젝트의 Pub/Sub 구독만 삭제할 수 있습니다.")
			}
			if err := s.store.DeletePubSubSubscription(resource); err != nil {
				return "", err
			}
			return fmt.Sprintf("Pub/Sub 구독 %s을(를) 삭제했습니다.", shortDashboardResourceName(resource)), nil
		default:
			return "", dashboardBadRequest("삭제할 Pub/Sub 리소스 종류가 올바르지 않습니다.")
		}
	default:
		return "", dashboardBadRequest("지원하지 않는 삭제 서비스입니다.")
	}
}

func (s *Server) pubSubDashboardResourceName(kind, value string) (string, error) {
	value = strings.TrimSpace(value)
	if !pubSubDashboardID.MatchString(value) || strings.HasPrefix(strings.ToLower(value), "goog") {
		return "", dashboardBadRequest("Pub/Sub 이름은 문자로 시작하는 3~255자의 안전한 ID여야 합니다.")
	}
	collection := "topics"
	if kind == "subscription" {
		collection = "subscriptions"
	}
	return fmt.Sprintf("projects/%s/%s/%s", s.projectID, collection, value), nil
}

func validS3DashboardBucket(value string) bool {
	return s3DashboardBucketName.MatchString(value) && !strings.Contains(value, "..") && !strings.Contains(value, ".-") && !strings.Contains(value, "-.")
}

func validGCSDashboardBucket(value string) bool {
	return gcsDashboardBucketName.MatchString(value) && !strings.Contains(value, "..")
}

func validSQSDashboardQueue(value string, fifo bool) bool {
	if len(value) < 1 || len(value) > 80 {
		return false
	}
	base := value
	if strings.HasSuffix(value, ".fifo") {
		base = strings.TrimSuffix(value, ".fifo")
	}
	return sqsDashboardQueueName.MatchString(base) && (strings.HasSuffix(value, ".fifo") == fifo)
}

func dashboardBool(value string) (bool, error) {
	if strings.TrimSpace(value) == "" {
		return false, nil
	}
	return strconv.ParseBool(value)
}

func dashboardBadRequest(message string) error {
	return &dashboardRequestError{status: http.StatusBadRequest, message: message}
}

func dashboardConflict(message string) error {
	return &dashboardRequestError{status: http.StatusConflict, message: message}
}

func shortDashboardResourceName(value string) string {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	return parts[len(parts)-1]
}

func writeDashboardActionError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": message})
}
