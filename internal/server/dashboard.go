package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed dashboard/*
var dashboardAssets embed.FS

type dashboardResponse struct {
	Project     string             `json:"project"`
	GeneratedAt time.Time          `json:"generatedAt"`
	Summary     dashboardSummary   `json:"summary"`
	Services    []dashboardService `json:"services"`
	Page        *dashboardPage     `json:"page,omitempty"`
}

type dashboardSummary struct {
	ServiceCount          int `json:"serviceCount"`
	AWSServiceCount       int `json:"awsServiceCount"`
	GCPServiceCount       int `json:"gcpServiceCount"`
	SDKVerifiedCount      int `json:"sdkVerifiedCount"`
	ContractVerifiedCount int `json:"contractVerifiedCount"`
	ResourceCount         int `json:"resourceCount"`
	MessageCount          int `json:"messageCount"`
}

type dashboardService struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Provider      string                `json:"provider"`
	Description   string                `json:"description"`
	Status        string                `json:"status"`
	Verification  dashboardVerification `json:"verification"`
	ResourceCount int                   `json:"resourceCount"`
	Resources     []dashboardResource   `json:"resources,omitempty"`
}

type dashboardVerification struct {
	Level       string                           `json:"level"`
	Label       string                           `json:"label"`
	Evidence    string                           `json:"evidence"`
	Source      string                           `json:"source"`
	Operations  []dashboardOperationVerification `json:"operations"`
	Limitations []string                         `json:"limitations,omitempty"`
}

type dashboardOperationVerification struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Scope  string `json:"scope"`
}

type dashboardPage struct {
	Service     string `json:"service"`
	Query       string `json:"query,omitempty"`
	Total       int    `json:"total"`
	Offset      int    `json:"offset"`
	Limit       int    `json:"limit"`
	HasPrevious bool   `json:"hasPrevious"`
	HasNext     bool   `json:"hasNext"`
}

type dashboardResource struct {
	Name       string               `json:"name"`
	Kind       string               `json:"kind"`
	Status     string               `json:"status"`
	CreatedAt  time.Time            `json:"createdAt,omitzero"`
	UpdatedAt  time.Time            `json:"updatedAt,omitzero"`
	Attributes []dashboardAttribute `json:"attributes,omitempty"`
}

type dashboardAttribute struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

func (s *Server) handleDashboardAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	dashboard := s.dashboardSnapshot()
	switch r.URL.Query().Get("view") {
	case "":
		// The unfiltered response is retained for API compatibility and diagnostics.
	case "summary":
		dashboard = dashboardSummaryView(dashboard)
	case "service":
		var err error
		dashboard, err = dashboardServiceView(dashboard, r.URL.Query().Get("service"), r.URL.Query().Get("q"), r.URL.Query().Get("limit"), r.URL.Query().Get("offset"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "unsupported dashboard view", http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(dashboard)
}

func (s *Server) handleDashboardUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	asset := strings.TrimPrefix(r.URL.Path, "/_fcp/ui")
	asset = strings.TrimPrefix(asset, "/")
	if asset == "" {
		asset = "index.html"
	}
	asset = path.Clean(asset)
	if asset == "." || strings.HasPrefix(asset, "../") {
		http.NotFound(w, r)
		return
	}
	body, err := dashboardAssets.ReadFile("dashboard/" + asset)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch path.Ext(asset) {
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

func (s *Server) dashboardSnapshot() dashboardResponse {
	services := []dashboardService{
		{ID: "s3", Name: "S3", Provider: "AWS", Description: "버킷과 객체 저장 상태", Status: "READY", Verification: dashboardVerificationFor("s3", "SDK", "AWS SDK JavaScript v3.1092.0"), Resources: []dashboardResource{}},
		{ID: "sqs", Name: "SQS", Provider: "AWS", Description: "큐와 대기 메시지 상태", Status: "READY", Verification: dashboardVerificationFor("sqs", "SDK", "AWS SDK JavaScript v3.1092.0 · Java v2.33.9"), Resources: []dashboardResource{}},
		{ID: "dynamodb", Name: "DynamoDB", Provider: "AWS", Description: "테이블 스키마와 저장 아이템 상태", Status: "READY", Verification: dashboardVerificationFor("dynamodb", "SDK", "AWS SDK Java v2.33.9"), Resources: []dashboardResource{}},
		{ID: "sts", Name: "STS", Provider: "AWS", Description: "로컬 AWS 호출자 identity", Status: "READY", Verification: dashboardVerificationFor("sts", "SDK", "AWS SDK Java v2.33.9"), Resources: []dashboardResource{}},
		{ID: "gcs", Name: "Cloud Storage", Provider: "GCP", Description: "버킷과 객체 저장 상태", Status: "READY", Verification: dashboardVerificationFor("gcs", "SDK", "Storage Go v1.64.0 · Java v2.68.0 · JavaScript v7.19.0"), Resources: []dashboardResource{}},
		{ID: "pubsub", Name: "Pub/Sub", Provider: "GCP", Description: "토픽, 구독과 미확인 메시지", Status: "READY", Verification: dashboardVerificationFor("pubsub", "SDK", "Pub/Sub Go v2.6.1 · Java v1.140.1 · JavaScript v4.11.0"), Resources: []dashboardResource{}},
		{ID: "firestore", Name: "Firestore", Provider: "GCP", Description: "저장된 문서 메타데이터", Status: "READY", Verification: dashboardVerificationFor("firestore", "SDK", "Firestore Go v1.24.0 · Kotlin/Java"), Resources: []dashboardResource{}},
		{ID: "secrets", Name: "Secret Manager", Provider: "GCP", Description: "값을 제외한 Secret과 버전 상태", Status: "READY", Verification: dashboardVerificationFor("secrets", "SDK", "Secret Manager Go v1.21.0 · Java v2.52.0/v2.59.0"), Resources: []dashboardResource{}},
		{ID: "kms", Name: "Cloud KMS", Provider: "GCP", Description: "키링, 키와 버전 상태", Status: "READY", Verification: dashboardVerificationFor("kms", "SDK", "Cloud KMS Go v1.31.0 · Java v2.96.0"), Resources: []dashboardResource{}},
		{ID: "iam", Name: "IAM Credentials", Provider: "GCP", Description: "개인키를 제외한 로컬 서비스 계정", Status: "READY", Verification: dashboardVerificationFor("iam", "SDK", "IAM Credentials Go v1.12.0 · Java v2.51.0"), Resources: []dashboardResource{}},
		{ID: "fcm", Name: "FCM", Provider: "GCP", Description: "외부 발송 없이 캡처된 요청", Status: "READY", Verification: dashboardVerificationFor("fcm", "CONTRACT", "FCM HTTP v1 요청 경로"), Resources: []dashboardResource{}},
		{ID: "metadata", Name: "Compute Metadata", Provider: "GCP", Description: "로컬 프로젝트와 서비스 계정 identity", Status: "READY", Verification: dashboardVerificationFor("metadata", "CONTRACT", "Metadata REST · JWKS 경로"), Resources: []dashboardResource{}},
		{ID: "vertex", Name: "Vertex AI", Provider: "GCP", Description: "모델 목록과 로컬 생성 호출 상태", Status: "READY", Verification: dashboardVerificationFor("vertex", "SDK", "Google Gen AI Java v1.58.0"), Resources: []dashboardResource{}},
	}
	byID := make(map[string]*dashboardService, len(services))
	for i := range services {
		byID[services[i].ID] = &services[i]
	}

	messageCount := 0
	for _, bucket := range s.store.ListBuckets() {
		totalBytes := int64(0)
		for _, object := range bucket.Objects {
			totalBytes += object.Size
		}
		multipartUploads, _ := s.store.ListMultipartUploads(bucket.Name, "")
		byID["s3"].Resources = append(byID["s3"].Resources, dashboardResource{
			Name: bucket.Name, Kind: "Bucket", Status: "READY", CreatedAt: bucket.CreatedAt,
			Attributes: attributes(
				"객체", strconv.Itoa(len(bucket.Objects)),
				"저장 용량", strconv.FormatInt(totalBytes, 10),
				"진행 중 업로드", strconv.Itoa(len(multipartUploads)),
				"이벤트 규칙", strconv.Itoa(len(bucket.Notifications)),
			),
		})
	}
	for _, queue := range s.store.ListQueues("") {
		messageCount += len(queue.Messages)
		queueType := "Standard"
		if strings.EqualFold(queue.Attributes["FifoQueue"], "true") {
			queueType = "FIFO"
		}
		resource := dashboardResource{
			Name: queue.Name, Kind: "Queue", Status: "READY", CreatedAt: queue.CreatedAt,
			Attributes: attributes(
				"유형", queueType,
				"메시지", strconv.Itoa(len(queue.Messages)),
				"Visibility timeout", queue.Attributes["VisibilityTimeout"],
			),
		}
		if queueType == "FIFO" {
			resource.Attributes = append(resource.Attributes, dashboardAttribute{Label: "Content dedup", Value: queue.Attributes["ContentBasedDeduplication"]})
		}
		resource.Attributes = append(resource.Attributes, queueRedriveAttributes(queue.Attributes["RedrivePolicy"])...)
		byID["sqs"].Resources = append(byID["sqs"].Resources, resource)
	}
	for _, table := range s.store.ListDynamoTables() {
		partitionKey, sortKey := "", ""
		for _, key := range table.KeySchema {
			switch key.KeyType {
			case "HASH":
				partitionKey = key.AttributeName
			case "RANGE":
				sortKey = key.AttributeName
			}
		}
		byID["dynamodb"].Resources = append(byID["dynamodb"].Resources, dashboardResource{
			Name: table.Name, Kind: "Table", Status: table.Status, CreatedAt: table.CreatedAt,
			Attributes: attributes(
				"아이템", strconv.Itoa(len(table.Items)),
				"Partition key", partitionKey,
				"Sort key", sortKey,
				"Billing", table.BillingMode,
			),
		})
	}
	byID["sts"].Resources = append(byID["sts"].Resources, dashboardResource{
		Name: "fcp-local", Kind: "Caller identity", Status: "READY",
		Attributes: attributes(
			"Account", "000000000000",
			"ARN", "arn:aws:iam::000000000000:user/fcp-local",
			"User ID", "AIDAFCPLOCAL000000000",
		),
	})
	for _, bucket := range s.store.ListGCSBuckets("") {
		totalBytes := int64(0)
		for _, object := range bucket.Objects {
			totalBytes += object.Size
		}
		byID["gcs"].Resources = append(byID["gcs"].Resources, dashboardResource{
			Name: bucket.Name, Kind: "Bucket", Status: "READY", CreatedAt: bucket.CreatedAt,
			Attributes: attributes(
				"프로젝트", bucket.Project,
				"리전", bucket.Location,
				"Storage class", bucket.StorageClass,
				"객체", strconv.Itoa(len(bucket.Objects)),
				"저장 용량", strconv.FormatInt(totalBytes, 10),
			),
		})
	}
	for _, topic := range s.store.ListPubSubTopics("") {
		byID["pubsub"].Resources = append(byID["pubsub"].Resources, dashboardResource{
			Name: topic.Name, Kind: "Topic", Status: "READY", CreatedAt: topic.CreatedAt,
			Attributes: attributes("라벨", strconv.Itoa(len(topic.Labels))),
		})
	}
	for _, subscription := range s.store.ListPubSubSubscriptions("") {
		messageCount += len(subscription.Messages)
		resource := dashboardResource{
			Name: subscription.Name, Kind: "Subscription", Status: "READY", CreatedAt: subscription.CreatedAt,
			Attributes: attributes(
				"토픽", subscription.Topic,
				"미확인 메시지", strconv.Itoa(len(subscription.Messages)),
				"Ack deadline", fmt.Sprintf("%ds", subscription.AckDeadlineSeconds),
			),
		}
		if subscription.DeadLetterTopic != "" {
			resource.Attributes = append(resource.Attributes, dashboardAttribute{Label: "Dead letter", Value: subscription.DeadLetterTopic})
		}
		byID["pubsub"].Resources = append(byID["pubsub"].Resources, resource)
	}
	for _, document := range s.store.ListFirestoreDocuments("") {
		byID["firestore"].Resources = append(byID["firestore"].Resources, dashboardResource{
			Name: document.Name, Kind: "Document", Status: "STORED", CreatedAt: document.CreateTime, UpdatedAt: document.UpdateTime,
			Attributes: attributes("직렬화 크기", strconv.Itoa(len(document.Proto))),
		})
	}
	for _, secret := range s.store.ListSecrets("") {
		states := map[string]int{}
		for _, version := range secret.Versions {
			states[version.State]++
		}
		byID["secrets"].Resources = append(byID["secrets"].Resources, dashboardResource{
			Name: secret.Name, Kind: "Secret", Status: secretStatus(states), CreatedAt: secret.CreateTime,
			Attributes: attributes(
				"버전", strconv.Itoa(len(secret.Versions)),
				"활성", strconv.Itoa(states["ENABLED"]),
				"비활성", strconv.Itoa(states["DISABLED"]),
				"삭제됨", strconv.Itoa(states["DESTROYED"]),
			),
		})
	}
	for _, keyRing := range s.store.ListKMSKeyRings("") {
		byID["kms"].Resources = append(byID["kms"].Resources, dashboardResource{
			Name: keyRing.Name, Kind: "Key ring", Status: "READY", CreatedAt: keyRing.CreateTime,
		})
	}
	for _, key := range s.store.ListKMSCryptoKeys("") {
		enabled := 0
		for _, version := range key.Versions {
			if version.State == "ENABLED" {
				enabled++
			}
		}
		byID["kms"].Resources = append(byID["kms"].Resources, dashboardResource{
			Name: key.Name, Kind: "Crypto key", Status: "ENABLED", CreatedAt: key.CreateTime,
			Attributes: attributes(
				"목적", key.Purpose,
				"알고리즘", key.Algorithm,
				"Primary version", strconv.FormatInt(key.PrimaryVersion, 10),
				"활성 버전", strconv.Itoa(enabled),
			),
		})
	}
	for _, account := range s.store.ListIAMServiceAccounts() {
		byID["iam"].Resources = append(byID["iam"].Resources, dashboardResource{
			Name: account.Name, Kind: "Service account", Status: "READY", CreatedAt: account.CreateTime,
		})
	}
	for _, message := range s.store.ListFCMMessages("") {
		messageCount++
		status := "CAPTURED"
		if message.ValidateOnly {
			status = "VALIDATED"
		}
		byID["fcm"].Resources = append(byID["fcm"].Resources, dashboardResource{
			Name: message.Name, Kind: "Message", Status: status, CreatedAt: message.CreatedAt,
			Attributes: attributes(
				"프로젝트", message.Project,
				"요청 크기", strconv.Itoa(len(message.Message)),
			),
		})
	}
	for _, generation := range s.store.ListVertexGenerations("") {
		byID["vertex"].Resources = append(byID["vertex"].Resources, dashboardResource{
			Name: generation.Name, Kind: "Generation", Status: "CAPTURED", CreatedAt: generation.CreatedAt,
			Attributes: attributes(
				"프로젝트", generation.Project,
				"리전", generation.Location,
				"모델", generation.Model,
				"호출", generation.Operation,
				"입력 문자", strconv.Itoa(generation.InputCharacters),
				"도구", strconv.Itoa(generation.ToolCount),
			),
		})
	}
	byID["metadata"].Resources = append(byID["metadata"].Resources, dashboardResource{
		Name: s.projectID, Kind: "Runtime identity", Status: "READY",
		Attributes: attributes(
			"서비스 계정", s.serviceAccountEmail,
			"Token TTL", "1h",
			"JWKS", "/oauth2/v3/certs",
		),
	})

	resourceCount := 0
	awsServiceCount := 0
	gcpServiceCount := 0
	sdkVerifiedCount := 0
	contractVerifiedCount := 0
	for i := range services {
		sort.SliceStable(services[i].Resources, func(a, b int) bool {
			if services[i].Resources[a].Kind == services[i].Resources[b].Kind {
				return services[i].Resources[a].Name < services[i].Resources[b].Name
			}
			return services[i].Resources[a].Kind < services[i].Resources[b].Kind
		})
		services[i].ResourceCount = len(services[i].Resources)
		resourceCount += services[i].ResourceCount
		switch services[i].Provider {
		case "AWS":
			awsServiceCount++
		case "GCP":
			gcpServiceCount++
		}
		switch services[i].Verification.Level {
		case "SDK":
			sdkVerifiedCount++
		case "CONTRACT":
			contractVerifiedCount++
		}
	}
	return dashboardResponse{
		Project:     s.projectID,
		GeneratedAt: time.Now().UTC(),
		Summary: dashboardSummary{
			ServiceCount: len(services), AWSServiceCount: awsServiceCount, GCPServiceCount: gcpServiceCount,
			SDKVerifiedCount: sdkVerifiedCount, ContractVerifiedCount: contractVerifiedCount,
			ResourceCount: resourceCount, MessageCount: messageCount,
		},
		Services: services,
	}
}

func dashboardSummaryView(dashboard dashboardResponse) dashboardResponse {
	dashboard.Page = nil
	for i := range dashboard.Services {
		dashboard.Services[i].Resources = nil
		dashboard.Services[i].Verification.Operations = nil
		dashboard.Services[i].Verification.Limitations = nil
	}
	return dashboard
}

func dashboardServiceView(dashboard dashboardResponse, serviceID, query, rawLimit, rawOffset string) (dashboardResponse, error) {
	if serviceID == "" {
		return dashboardResponse{}, fmt.Errorf("service is required")
	}
	limit, err := dashboardPageNumber(rawLimit, 25, 1, 100, "limit")
	if err != nil {
		return dashboardResponse{}, err
	}
	offset, err := dashboardPageNumber(rawOffset, 0, 0, 1_000_000, "offset")
	if err != nil {
		return dashboardResponse{}, err
	}

	found := false
	query = strings.TrimSpace(query)
	for i := range dashboard.Services {
		service := &dashboard.Services[i]
		if service.ID != serviceID {
			service.Resources = nil
			continue
		}
		found = true
		filtered := make([]dashboardResource, 0, len(service.Resources))
		for _, resource := range service.Resources {
			if dashboardResourceMatches(*service, resource, query) {
				filtered = append(filtered, resource)
			}
		}
		total := len(filtered)
		start := min(offset, total)
		end := min(start+limit, total)
		service.Resources = filtered[start:end]
		dashboard.Page = &dashboardPage{
			Service: serviceID, Query: query, Total: total, Offset: start, Limit: limit,
			HasPrevious: start > 0, HasNext: end < total,
		}
	}
	if !found {
		return dashboardResponse{}, fmt.Errorf("unknown service %q", serviceID)
	}
	return dashboard, nil
}

func dashboardPageNumber(raw string, fallback, minimum, maximum int, name string) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return value, nil
}

func dashboardResourceMatches(service dashboardService, resource dashboardResource, query string) bool {
	if query == "" {
		return true
	}
	parts := []string{service.Name, service.Provider, resource.Name, resource.Kind, resource.Status}
	for _, attribute := range resource.Attributes {
		parts = append(parts, attribute.Label, attribute.Value)
	}
	return strings.Contains(strings.ToLower(strings.Join(parts, " ")), strings.ToLower(query))
}

func attributes(values ...string) []dashboardAttribute {
	result := make([]dashboardAttribute, 0, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		if values[i+1] == "" {
			continue
		}
		result = append(result, dashboardAttribute{Label: values[i], Value: values[i+1]})
	}
	return result
}

func secretStatus(states map[string]int) string {
	if states["ENABLED"] > 0 {
		return "ENABLED"
	}
	if states["DISABLED"] > 0 {
		return "DISABLED"
	}
	if states["DESTROYED"] > 0 {
		return "DESTROYED"
	}
	return "EMPTY"
}

func queueRedriveAttributes(raw string) []dashboardAttribute {
	if raw == "" {
		return nil
	}
	var policy struct {
		DeadLetterTargetARN string          `json:"deadLetterTargetArn"`
		MaxReceiveCount     json.RawMessage `json:"maxReceiveCount"`
	}
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return nil
	}
	parts := strings.Split(policy.DeadLetterTargetARN, ":")
	target := policy.DeadLetterTargetARN
	if len(parts) > 0 {
		target = parts[len(parts)-1]
	}
	maxReceives := strings.Trim(string(policy.MaxReceiveCount), `"`)
	return attributes("Dead letter", target, "Max receives", maxReceives)
}
