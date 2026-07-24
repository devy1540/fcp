package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hjyoon/fcp/internal/profile"
	"github.com/hjyoon/fcp/internal/state"
)

func TestDashboardListsResourcesWithoutSensitiveValues(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profile.SeedDemo(store, "fcp-local"); err != nil {
		t.Fatal(err)
	}
	secret, err := store.CreateSecret("projects/fcp-local/secrets/dashboard-private", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSecretVersion(secret.Name, []byte("do-not-expose-secret-payload")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IAMServiceAccount("projects/-/serviceAccounts/dashboard@fcp-local.iam.gserviceaccount.com", func() ([]byte, error) {
		return []byte("do-not-expose-private-key"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateKMSCryptoKey(state.KMSCryptoKey{
		Name:    "projects/fcp-local/locations/asia-northeast3/keyRings/fcp-local/cryptoKeys/dashboard-key",
		Purpose: "ENCRYPT_DECRYPT", Algorithm: "GOOGLE_SYMMETRIC_ENCRYPTION", PrimaryVersion: 1, CreateTime: time.Now().UTC(),
		Versions: []state.KMSKeyVersion{{Number: 1, Algorithm: "GOOGLE_SYMMETRIC_ENCRYPTION", State: "ENABLED", KeyMaterial: []byte("do-not-expose-key-material"), CreateTime: time.Now().UTC()}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordFCMMessage("fcp-local", json.RawMessage(`{"token":"do-not-expose-device-token","notification":{"title":"private"}}`), false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordVertexGeneration("fcp-local", "global", "gemini-2.5-flash", "generateContent", 24, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("dashboard-s3"); err != nil {
		t.Fatal(err)
	}
	upload, err := store.CreateMultipartUpload("dashboard-s3", "private.bin", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UploadMultipartPart("dashboard-s3", "private.bin", upload.ID, 1, []byte("do-not-expose-multipart-body")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateQueue("dashboard-dlq", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateQueue("dashboard-jobs", map[string]string{
		"RedrivePolicy": `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:dashboard-dlq","maxReceiveCount":"5"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateQueue("dashboard-orders.fifo", map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDynamoTable("dashboard-table",
		[]state.DynamoKeySchemaElement{{AttributeName: "pk", KeyType: "HASH"}, {AttributeName: "sk", KeyType: "RANGE"}},
		[]state.DynamoAttributeDefinition{{AttributeName: "pk", AttributeType: "S"}, {AttributeName: "sk", AttributeType: "S"}},
		"PAY_PER_REQUEST"); err != nil {
		t.Fatal(err)
	}
	dynamoPK, dynamoSK, dynamoSecret := "APP#dashboard", "CHECK", "do-not-expose-dynamo-item"
	if _, _, err := store.DynamoPutItem("dashboard-table", state.DynamoItem{
		"pk": {S: &dynamoPK}, "sk": {S: &dynamoSK}, "payload": {S: &dynamoSecret},
	}, nil); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(NewWithOptions(store, Options{ProjectID: "fcp-local"}))
	defer server.Close()
	response, err := http.Get(server.URL + "/_fcp/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("dashboard status=%d content-type=%q body=%s", response.StatusCode, response.Header.Get("Content-Type"), body)
	}
	for _, sensitive := range []string{
		"do-not-expose-secret-payload",
		"do-not-expose-private-key",
		"do-not-expose-key-material",
		"do-not-expose-device-token",
		"do-not-expose-multipart-body",
		"do-not-expose-dynamo-item",
	} {
		if strings.Contains(string(body), sensitive) {
			t.Fatalf("dashboard exposed sensitive value %q", sensitive)
		}
	}

	var dashboard dashboardResponse
	if err := json.Unmarshal(body, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.Project != "fcp-local" || dashboard.Summary.ServiceCount != 13 {
		t.Fatalf("unexpected dashboard summary: %+v", dashboard)
	}
	if dashboard.Summary.AWSServiceCount != 4 || dashboard.Summary.GCPServiceCount != 9 || dashboard.Summary.SDKVerifiedCount != 11 || dashboard.Summary.ContractVerifiedCount != 2 {
		t.Fatalf("unexpected provider or verification summary: %+v", dashboard.Summary)
	}
	for _, service := range dashboard.Services {
		if service.Provider != "AWS" && service.Provider != "GCP" {
			t.Fatalf("service %s has unknown provider %q", service.ID, service.Provider)
		}
		if service.Verification.Level == "" || service.Verification.Label == "" || service.Verification.Evidence == "" || service.Verification.Source != "docs/compatibility.md" || len(service.Verification.Operations) == 0 {
			t.Fatalf("service %s is missing verification evidence: %+v", service.ID, service.Verification)
		}
		for _, operation := range service.Verification.Operations {
			if operation.Name == "" || operation.Scope == "" || (operation.Status != "FULL" && operation.Status != "PARTIAL") {
				t.Fatalf("service %s has invalid operation verification: %+v", service.ID, operation)
			}
		}
		if service.ResourceCount != len(service.Resources) {
			t.Fatalf("service %s resource count=%d resources=%d", service.ID, service.ResourceCount, len(service.Resources))
		}
	}
	secretService := findDashboardService(t, dashboard.Services, "secrets")
	if len(secretService.Resources) == 0 {
		t.Fatal("dashboard did not list Secret Manager resources")
	}
	fcmService := findDashboardService(t, dashboard.Services, "fcm")
	if len(fcmService.Resources) != 1 || dashboard.Summary.MessageCount != 1 {
		t.Fatalf("unexpected FCM dashboard data: service=%+v summary=%+v", fcmService, dashboard.Summary)
	}
	vertexService := findDashboardService(t, dashboard.Services, "vertex")
	if len(vertexService.Resources) != 1 || !hasDashboardAttribute(vertexService.Resources[0], "모델", "gemini-2.5-flash") || !hasDashboardAttribute(vertexService.Resources[0], "입력 문자", "24") {
		t.Fatalf("unexpected Vertex AI dashboard data: %+v", vertexService)
	}
	if vertexService.Verification.Level != "SDK" || !strings.Contains(vertexService.Verification.Evidence, "Google Gen AI Java v1.58.0") {
		t.Fatalf("Vertex AI verification evidence is incorrect: %+v", vertexService.Verification)
	}
	fcmVerification := fcmService.Verification
	if fcmVerification.Level != "CONTRACT" || !strings.Contains(fcmVerification.Evidence, "FCM HTTP v1") {
		t.Fatalf("FCM verification evidence is incorrect: %+v", fcmVerification)
	}
	s3Service := findDashboardService(t, dashboard.Services, "s3")
	if len(s3Service.Resources) != 1 || !hasDashboardAttribute(s3Service.Resources[0], "진행 중 업로드", "1") {
		t.Fatalf("dashboard did not report active multipart upload: %+v", s3Service)
	}
	sqsService := findDashboardService(t, dashboard.Services, "sqs")
	jobs := findDashboardResource(t, sqsService.Resources, "dashboard-jobs")
	if !hasDashboardAttribute(jobs, "Dead letter", "dashboard-dlq") || !hasDashboardAttribute(jobs, "Max receives", "5") {
		t.Fatalf("dashboard did not report SQS redrive policy: %+v", jobs)
	}
	orders := findDashboardResource(t, sqsService.Resources, "dashboard-orders.fifo")
	if !hasDashboardAttribute(orders, "유형", "FIFO") || !hasDashboardAttribute(orders, "Content dedup", "true") {
		t.Fatalf("dashboard did not report FIFO configuration: %+v", orders)
	}
	dynamoService := findDashboardService(t, dashboard.Services, "dynamodb")
	dynamoTable := findDashboardResource(t, dynamoService.Resources, "dashboard-table")
	if !hasDashboardAttribute(dynamoTable, "아이템", "1") || !hasDashboardAttribute(dynamoTable, "Partition key", "pk") || !hasDashboardAttribute(dynamoTable, "Sort key", "sk") {
		t.Fatalf("dashboard did not report DynamoDB table metadata: %+v", dynamoTable)
	}
	stsService := findDashboardService(t, dashboard.Services, "sts")
	if len(stsService.Resources) != 1 || !hasDashboardAttribute(stsService.Resources[0], "Account", "000000000000") {
		t.Fatalf("dashboard did not report STS identity: %+v", stsService)
	}
}

func TestDashboardSummaryAndServicePagination(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 31; index++ {
		if _, err := store.CreateGCSBucket("fcp-local", fmt.Sprintf("dashboard-page-%02d", index), "ASIA", "STANDARD"); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(NewWithOptions(store, Options{ProjectID: "fcp-local"}))
	defer server.Close()

	summary := getDashboard(t, server.URL+"/_fcp/dashboard?view=summary")
	if summary.Page != nil {
		t.Fatalf("summary unexpectedly contains page metadata: %+v", summary.Page)
	}
	gcsSummary := findDashboardService(t, summary.Services, "gcs")
	if gcsSummary.ResourceCount != 31 || len(gcsSummary.Resources) != 0 {
		t.Fatalf("summary should contain counts without resources: %+v", gcsSummary)
	}
	for _, service := range summary.Services {
		if len(service.Resources) != 0 {
			t.Fatalf("summary service %s unexpectedly contains resources", service.ID)
		}
		if len(service.Verification.Operations) != 0 || len(service.Verification.Limitations) != 0 {
			t.Fatalf("summary service %s unexpectedly contains verification details", service.ID)
		}
	}

	first := getDashboard(t, server.URL+"/_fcp/dashboard?view=service&service=gcs&limit=10")
	firstGCS := findDashboardService(t, first.Services, "gcs")
	if first.Page == nil || first.Page.Total != 31 || first.Page.Offset != 0 || first.Page.Limit != 10 || first.Page.HasPrevious || !first.Page.HasNext {
		t.Fatalf("unexpected first page: %+v", first.Page)
	}
	if len(firstGCS.Resources) != 10 || firstGCS.ResourceCount != 31 || firstGCS.Resources[0].Name != "dashboard-page-00" {
		t.Fatalf("unexpected first page resources: %+v", firstGCS)
	}
	if len(findDashboardService(t, first.Services, "s3").Resources) != 0 {
		t.Fatal("service view should not include another service's resources")
	}

	last := getDashboard(t, server.URL+"/_fcp/dashboard?view=service&service=gcs&limit=10&offset=30&q=page-")
	lastGCS := findDashboardService(t, last.Services, "gcs")
	if last.Page == nil || last.Page.Total != 31 || last.Page.Offset != 30 || !last.Page.HasPrevious || last.Page.HasNext || len(lastGCS.Resources) != 1 {
		t.Fatalf("unexpected last page: page=%+v resources=%+v", last.Page, lastGCS.Resources)
	}

	response, err := http.Get(server.URL + "/_fcp/dashboard?view=service&service=unknown")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown service status=%d want=%d", response.StatusCode, http.StatusBadRequest)
	}
}

func getDashboard(t *testing.T, endpoint string) dashboardResponse {
	t.Helper()
	response, err := http.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("dashboard status=%d body=%s", response.StatusCode, body)
	}
	var dashboard dashboardResponse
	if err := json.NewDecoder(response.Body).Decode(&dashboard); err != nil {
		t.Fatal(err)
	}
	return dashboard
}

func TestDashboardUIAssetsAreEmbedded(t *testing.T) {
	server := newTestServer(t)
	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/_fcp/ui", contentType: "text/html", contains: "FCP Console"},
		{path: "/_fcp/ui/styles.css", contentType: "text/css", contains: ".resource-panel"},
		{path: "/_fcp/ui/app.js", contentType: "text/javascript", contains: "loadDashboard"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response, err := http.Get(server.URL + test.path)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), test.contentType) {
				t.Fatalf("status=%d content-type=%q", response.StatusCode, response.Header.Get("Content-Type"))
			}
			if !strings.Contains(string(body), test.contains) {
				t.Fatalf("asset %s is missing %q", test.path, test.contains)
			}
			if strings.Contains(response.Header.Get("Content-Security-Policy"), "unsafe-inline") {
				t.Fatal("dashboard CSP unexpectedly allows unsafe inline assets")
			}
		})
	}
	index, err := dashboardAssets.ReadFile("dashboard/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, accessibleName := range []string{`aria-label="새로고침"`, `aria-label="테스트 데이터 비우기"`, `aria-label="리소스 검색"`, `aria-label="클라우드 제공자 필터"`, `id="verification-note"`, `aria-labelledby="confirm-title"`, `aria-labelledby="create-title"`, `role="alert"`} {
		if !strings.Contains(string(index), accessibleName) {
			t.Fatalf("dashboard is missing %s", accessibleName)
		}
	}
}

func TestDashboardActionsPurgeMessagesAndResetWorkload(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("assets", "hello.txt", []byte("hello"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateQueue("jobs", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SendMessage("jobs", "job", nil, 0); err != nil {
		t.Fatal(err)
	}
	topic := "projects/fcp-local/topics/events"
	subscription := "projects/fcp-local/subscriptions/worker"
	if _, err := store.CreatePubSubTopic(topic, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePubSubSubscription(subscription, topic, 10, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishPubSub(topic, []state.PubSubMessage{{Data: []byte("event")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordFCMMessage("fcp-local", json.RawMessage(`{"token":"local"}`), false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordVertexGeneration("fcp-local", "global", "gemini-2.5-flash", "generateContent", 5, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDynamoTable("local-state",
		[]state.DynamoKeySchemaElement{{AttributeName: "pk", KeyType: "HASH"}},
		[]state.DynamoAttributeDefinition{{AttributeName: "pk", AttributeType: "S"}}, "PAY_PER_REQUEST"); err != nil {
		t.Fatal(err)
	}
	localKey := "item"
	if _, _, err := store.DynamoPutItem("local-state", state.DynamoItem{"pk": {S: &localKey}}, nil); err != nil {
		t.Fatal(err)
	}
	secretName := "projects/fcp-local/secrets/config"
	if _, err := store.CreateSecret(secretName, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSecretVersion(secretName, []byte("preserved")); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(NewWithOptions(store, Options{ProjectID: "fcp-local"}))
	defer server.Close()
	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "purge", Service: "sqs", Resource: "jobs"})
	queue, err := store.Queue("jobs")
	if err != nil || len(queue.Messages) != 0 {
		t.Fatalf("SQS purge action failed: queue=%+v err=%v", queue, err)
	}
	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "purge", Service: "pubsub", Resource: subscription})
	storedSubscription, err := store.PubSubSubscription(subscription)
	if err != nil || len(storedSubscription.Messages) != 0 {
		t.Fatalf("Pub/Sub purge action failed: subscription=%+v err=%v", storedSubscription, err)
	}
	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "purge", Service: "fcm"})
	if len(store.ListFCMMessages("")) != 0 {
		t.Fatal("FCM purge action failed")
	}
	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "purge", Service: "vertex"})
	if len(store.ListVertexGenerations("")) != 0 {
		t.Fatal("Vertex AI purge action failed")
	}
	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "purge", Service: "dynamodb", Resource: "local-state"})
	dynamoItems, err := store.DynamoListItems("local-state")
	if err != nil || len(dynamoItems) != 0 {
		t.Fatalf("DynamoDB purge action failed: items=%+v err=%v", dynamoItems, err)
	}
	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "reset-workload"})
	objects, _, err := store.ListObjects("assets", "", "", 0)
	if err != nil || len(objects) != 0 {
		t.Fatalf("workload reset did not clear objects: objects=%+v err=%v", objects, err)
	}
	version, err := store.SecretVersion(secretName, 1)
	if err != nil || string(version.Payload) != "preserved" {
		t.Fatalf("workload reset changed secret: version=%+v err=%v", version, err)
	}
}

func TestDashboardActionsCreateAndDeleteResources(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewWithOptions(store, Options{ProjectID: "fcp-local"}))
	defer server.Close()

	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "create", Service: "s3", Kind: "bucket", Resource: "dashboard-assets"})
	if !store.HasBucket("dashboard-assets") {
		t.Fatal("dashboard did not create S3 bucket")
	}
	dashboardActionStatus(t, server.URL, dashboardActionRequest{Operation: "create", Service: "s3", Kind: "bucket", Resource: "dashboard-assets"}, http.StatusConflict)
	dashboardActionStatus(t, server.URL, dashboardActionRequest{Operation: "create", Service: "s3", Kind: "bucket", Resource: "INVALID_BUCKET"}, http.StatusBadRequest)

	dashboardAction(t, server.URL, dashboardActionRequest{
		Operation: "create", Service: "sqs", Kind: "queue", Resource: "dashboard-orders.fifo",
		Parameters: map[string]string{"queueType": "fifo", "contentBasedDeduplication": "true"},
	})
	dashboardAction(t, server.URL, dashboardActionRequest{
		Operation: "create", Service: "dynamodb", Kind: "table", Resource: "dashboard-state",
		Parameters: map[string]string{"partitionKey": "pk", "sortKey": "sk"},
	})
	dynamoTable, err := store.DynamoTable("dashboard-state")
	if err != nil || len(dynamoTable.KeySchema) != 2 || dynamoTable.BillingMode != "PAY_PER_REQUEST" {
		t.Fatalf("dashboard did not create DynamoDB table: table=%+v err=%v", dynamoTable, err)
	}
	dashboardActionStatus(t, server.URL, dashboardActionRequest{
		Operation: "create", Service: "dynamodb", Kind: "table", Resource: "dashboard-state",
		Parameters: map[string]string{"partitionKey": "pk", "sortKey": "sk"},
	}, http.StatusConflict)
	queue, err := store.Queue("dashboard-orders.fifo")
	if err != nil || queue.Attributes["FifoQueue"] != "true" || queue.Attributes["ContentBasedDeduplication"] != "true" {
		t.Fatalf("dashboard did not create FIFO queue: queue=%+v err=%v", queue, err)
	}

	dashboardAction(t, server.URL, dashboardActionRequest{
		Operation: "create", Service: "gcs", Kind: "bucket", Resource: "dashboard-gcs",
		Parameters: map[string]string{"location": "asia-northeast3", "storageClass": "NEARLINE"},
	})
	gcsBucket, err := store.GCSBucket("dashboard-gcs")
	if err != nil || gcsBucket.Project != "fcp-local" || gcsBucket.Location != "ASIA-NORTHEAST3" || gcsBucket.StorageClass != "NEARLINE" {
		t.Fatalf("dashboard did not create GCS bucket: bucket=%+v err=%v", gcsBucket, err)
	}

	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "create", Service: "pubsub", Kind: "topic", Resource: "dashboard-events"})
	topic := "projects/fcp-local/topics/dashboard-events"
	if _, err := store.PubSubTopic(topic); err != nil {
		t.Fatalf("dashboard did not create Pub/Sub topic: %v", err)
	}
	dashboardAction(t, server.URL, dashboardActionRequest{
		Operation: "create", Service: "pubsub", Kind: "subscription", Resource: "dashboard-worker",
		Parameters: map[string]string{"topic": topic, "ackDeadlineSeconds": "30", "enableOrdering": "true"},
	})
	subscription := "projects/fcp-local/subscriptions/dashboard-worker"
	storedSubscription, err := store.PubSubSubscription(subscription)
	if err != nil || storedSubscription.Topic != topic || storedSubscription.AckDeadlineSeconds != 30 || !storedSubscription.EnableOrdering {
		t.Fatalf("dashboard did not create Pub/Sub subscription: subscription=%+v err=%v", storedSubscription, err)
	}

	if _, err := store.PutObject("dashboard-assets", "blocked.txt", []byte("blocked"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	dashboardActionStatus(t, server.URL, dashboardActionRequest{Operation: "delete", Service: "s3", Kind: "bucket", Resource: "dashboard-assets"}, http.StatusConflict)
	if err := store.DeleteObject("dashboard-assets", "blocked.txt"); err != nil {
		t.Fatal(err)
	}
	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "delete", Service: "s3", Kind: "bucket", Resource: "dashboard-assets"})
	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "delete", Service: "sqs", Kind: "queue", Resource: "dashboard-orders.fifo"})
	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "delete", Service: "gcs", Kind: "bucket", Resource: "dashboard-gcs"})
	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "delete", Service: "dynamodb", Kind: "table", Resource: "dashboard-state"})
	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "delete", Service: "pubsub", Kind: "subscription", Resource: subscription})
	dashboardAction(t, server.URL, dashboardActionRequest{Operation: "delete", Service: "pubsub", Kind: "topic", Resource: topic})

	if store.HasBucket("dashboard-assets") {
		t.Fatal("dashboard did not delete S3 bucket")
	}
	if _, err := store.Queue("dashboard-orders.fifo"); !errors.Is(err, state.ErrQueueNotFound) {
		t.Fatalf("dashboard did not delete SQS queue: %v", err)
	}
	if _, err := store.GCSBucket("dashboard-gcs"); !errors.Is(err, state.ErrGCSBucketNotFound) {
		t.Fatalf("dashboard did not delete GCS bucket: %v", err)
	}
	if _, err := store.DynamoTable("dashboard-state"); !errors.Is(err, state.ErrDynamoTableNotFound) {
		t.Fatalf("dashboard did not delete DynamoDB table: %v", err)
	}
	if _, err := store.PubSubSubscription(subscription); !errors.Is(err, state.ErrPubSubSubscriptionNotFound) {
		t.Fatalf("dashboard did not delete Pub/Sub subscription: %v", err)
	}
	if _, err := store.PubSubTopic(topic); !errors.Is(err, state.ErrPubSubTopicNotFound) {
		t.Fatalf("dashboard did not delete Pub/Sub topic: %v", err)
	}
}

func TestDashboardActionRequiresJSONAndRejectsUnknownActions(t *testing.T) {
	server := newTestServer(t)
	response, err := http.Post(server.URL+"/_fcp/actions", "text/plain", strings.NewReader(`{"operation":"reset-workload"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON dashboard action status = %d", response.StatusCode)
	}

	raw, _ := json.Marshal(dashboardActionRequest{Operation: "delete-everything"})
	request, err := http.NewRequest(http.MethodPost, server.URL+"/_fcp/actions", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown dashboard action status = %d", response.StatusCode)
	}
}

func dashboardAction(t *testing.T, endpoint string, action dashboardActionRequest) {
	t.Helper()
	dashboardActionStatus(t, endpoint, action, http.StatusOK)
}

func dashboardActionStatus(t *testing.T, endpoint string, action dashboardActionRequest, expectedStatus int) []byte {
	t.Helper()
	raw, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint+"/_fcp/actions", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != expectedStatus {
		t.Fatalf("dashboard action %+v status=%d want=%d body=%s", action, response.StatusCode, expectedStatus, body)
	}
	return body
}

func findDashboardService(t *testing.T, services []dashboardService, id string) dashboardService {
	t.Helper()
	for _, service := range services {
		if service.ID == id {
			return service
		}
	}
	t.Fatalf("dashboard service %q not found", id)
	return dashboardService{}
}

func hasDashboardAttribute(resource dashboardResource, label, value string) bool {
	for _, attribute := range resource.Attributes {
		if attribute.Label == label && attribute.Value == value {
			return true
		}
	}
	return false
}

func findDashboardResource(t *testing.T, resources []dashboardResource, name string) dashboardResource {
	t.Helper()
	for _, resource := range resources {
		if resource.Name == name {
			return resource
		}
	}
	t.Fatalf("dashboard resource %q not found", name)
	return dashboardResource{}
}
