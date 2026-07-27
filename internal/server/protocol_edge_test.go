package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/devy1540/fcp/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func TestGCSProtocolErrorsAndResumableProgress(t *testing.T) {
	server := newTestServer(t)
	assertHTTPStatus(t, http.MethodPatch, server.URL+"/storage/v1/b", nil, nil, http.StatusMethodNotAllowed, "methodNotAllowed")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/storage/v1/b", strings.NewReader("{"), nil, http.StatusBadRequest, "invalid")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/storage/v1/b", strings.NewReader(`{}`), nil, http.StatusBadRequest, "required")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/storage/v1/b/missing", nil, nil, http.StatusNotFound, "notFound")
	assertHTTPStatus(t, http.MethodDelete, server.URL+"/storage/v1/b/missing", nil, nil, http.StatusNotFound, "notFound")
	assertHTTPStatus(t, http.MethodPatch, server.URL+"/storage/v1/b/missing", nil, nil, http.StatusMethodNotAllowed, "methodNotAllowed")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/storage/v1/b/missing/o", nil, nil, http.StatusMethodNotAllowed, "methodNotAllowed")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/storage/v1/b/missing/o", nil, nil, http.StatusNotFound, "notFound")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/storage/v1/b/missing/o/key", nil, nil, http.StatusNotFound, "notFound")
	assertHTTPStatus(t, http.MethodPatch, server.URL+"/storage/v1/b/missing/o/key", strings.NewReader("{"), nil, http.StatusBadRequest, "invalid")
	assertHTTPStatus(t, http.MethodPatch, server.URL+"/storage/v1/b/missing/o/key", strings.NewReader(`{}`), nil, http.StatusNotFound, "notFound")
	assertHTTPStatus(t, http.MethodDelete, server.URL+"/storage/v1/b/missing/o/key", nil, nil, http.StatusNotFound, "notFound")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/storage/v1/b/missing/o/key", nil, nil, http.StatusMethodNotAllowed, "methodNotAllowed")
	assertHTTPStatus(t, http.MethodPut, server.URL+"/upload/storage/v1/b/missing/o", nil, nil, http.StatusMethodNotAllowed, "methodNotAllowed")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/upload/storage/v1/b/missing/o?uploadType=unknown&name=key", nil, nil, http.StatusBadRequest, "Unsupported uploadType")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/upload/storage/v1/b/missing/o?uploadType=media", strings.NewReader("body"), nil, http.StatusBadRequest, "Object name is required")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/upload/storage/v1/b/missing/o?uploadType=media&name=key", strings.NewReader("body"), nil, http.StatusNotFound, "notFound")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/upload/storage/v1/b/missing/o?uploadType=multipart", strings.NewReader("invalid"), map[string]string{"Content-Type": "text/plain"}, http.StatusBadRequest, "invalid multipart upload")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/_fcp/gcs-upload/missing", strings.NewReader("body"), nil, http.StatusNotFound, "Upload session not found")

	assertHTTPStatus(t, http.MethodPost, server.URL+"/storage/v1/b?project=test", strings.NewReader(`{"name":"uploads"}`), map[string]string{"Content-Type": "application/json"}, http.StatusOK, `"name":"uploads"`)
	assertHTTPStatus(t, http.MethodPost, server.URL+"/upload/storage/v1/b/uploads/o?uploadType=media&name=metadata.txt", strings.NewReader("metadata"), map[string]string{"Content-Type": "text/plain"}, http.StatusOK, `"name":"metadata.txt"`)
	assertHTTPStatus(t, http.MethodPost, server.URL+"/storage/v1/b/uploads/o/metadata.txt", strings.NewReader(`{"metadata":{"verified":"true"}}`), map[string]string{
		"Content-Type":           "application/json",
		"X-HTTP-Method-Override": "PATCH",
	}, http.StatusOK, `"verified":"true"`)
	start := doHTTPRequest(t, http.MethodPost, server.URL+"/upload/storage/v1/b/uploads/o?uploadType=resumable&name=large.bin", strings.NewReader(`{"contentType":"application/octet-stream"}`), map[string]string{"Content-Type": "application/json"})
	startBody, _ := io.ReadAll(start.Body)
	start.Body.Close()
	if start.StatusCode != http.StatusOK || start.Header.Get("Location") == "" {
		t.Fatalf("unexpected resumable start: status=%d location=%q body=%s", start.StatusCode, start.Header.Get("Location"), startBody)
	}
	progress := doHTTPRequest(t, http.MethodPost, start.Header.Get("Location"), strings.NewReader("part"), map[string]string{
		"Content-Range":      "bytes 0-3/*",
		"X-GUploader-No-308": "yes",
	})
	progress.Body.Close()
	if progress.StatusCode != http.StatusOK || progress.Header.Get("X-Http-Status-Code-Override") != "308" || progress.Header.Get("Range") != "bytes=0-3" {
		t.Fatalf("unexpected resumable progress: status=%d override=%q range=%q", progress.StatusCode, progress.Header.Get("X-Http-Status-Code-Override"), progress.Header.Get("Range"))
	}
	finished := doHTTPRequest(t, http.MethodPost, start.Header.Get("Location"), strings.NewReader("ial"), map[string]string{"Content-Range": "bytes 4-6/7"})
	finishedBody, _ := io.ReadAll(finished.Body)
	finished.Body.Close()
	if finished.StatusCode != http.StatusOK || !bytes.Contains(finishedBody, []byte(`"size":"7"`)) {
		t.Fatalf("unexpected resumable completion: status=%d body=%s", finished.StatusCode, finishedBody)
	}
	assertHTTPStatus(t, http.MethodPost, start.Header.Get("Location"), nil, nil, http.StatusNotFound, "Upload session not found")

	assertHTTPStatus(t, http.MethodDelete, server.URL+"/storage/v1/b/uploads", nil, nil, http.StatusConflict, "not empty")
	assertHTTPStatus(t, http.MethodDelete, server.URL+"/storage/v1/b/uploads/o/metadata.txt", nil, nil, http.StatusNoContent, "")
	assertHTTPStatus(t, http.MethodDelete, server.URL+"/storage/v1/b/uploads/o/large.bin", nil, nil, http.StatusNoContent, "")
	assertHTTPStatus(t, http.MethodDelete, server.URL+"/storage/v1/b/uploads", nil, nil, http.StatusNoContent, "")
}

func TestGCSXMLMediaAndRangeEdges(t *testing.T) {
	store := openServerTestStore(t)
	handler := New(store).(*Server)

	for _, test := range []struct {
		name   string
		method string
		target string
		status int
	}{
		{name: "method", method: http.MethodPost, target: "/bucket/key", status: http.StatusMethodNotAllowed},
		{name: "missing path", method: http.MethodGet, target: "/bucket", status: http.StatusNotFound},
		{name: "missing object", method: http.MethodGet, target: "/bucket/key", status: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.handleGCSXMLMedia(recorder, httptest.NewRequest(test.method, test.target, nil))
			if recorder.Code != test.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
			}
		})
	}

	if start, end, partial := parseRange("", 0); start != 0 || end != -1 || partial {
		t.Fatalf("unexpected empty range: start=%d end=%d partial=%v", start, end, partial)
	}
	if _, _, partial := parseRange("bytes=99-100", 3); partial {
		t.Fatal("out-of-range start should be ignored")
	}
	if start, end, partial := parseRange("bytes=1-invalid", 3); start != 1 || end != 2 || !partial {
		t.Fatalf("invalid end should fall back to object end: start=%d end=%d partial=%v", start, end, partial)
	}
}

func TestS3ProtocolErrorBranches(t *testing.T) {
	server := newTestServer(t)
	assertHTTPStatus(t, http.MethodPost, server.URL+"/", nil, nil, http.StatusMethodNotAllowed, "MethodNotAllowed")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/missing?uploads", nil, nil, http.StatusMethodNotAllowed, "MethodNotAllowed")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/missing/key?uploads", nil, nil, http.StatusMethodNotAllowed, "MethodNotAllowed")
	assertHTTPStatus(t, http.MethodPatch, server.URL+"/missing/key?uploadId=unknown", nil, nil, http.StatusMethodNotAllowed, "MethodNotAllowed")
	assertHTTPStatus(t, http.MethodPatch, server.URL+"/missing/key", nil, nil, http.StatusMethodNotAllowed, "MethodNotAllowed")
	assertHTTPStatus(t, http.MethodPut, server.URL+"/missing/key", strings.NewReader("body"), nil, http.StatusNotFound, "NoSuchBucket")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/missing/key?uploads", nil, nil, http.StatusNotFound, "NoSuchBucket")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/missing?uploads", nil, nil, http.StatusNotFound, "NoSuchBucket")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/missing/key", nil, nil, http.StatusNotFound, "NoSuchBucket")
	assertHTTPStatus(t, http.MethodDelete, server.URL+"/missing/key", nil, nil, http.StatusNotFound, "NoSuchBucket")
	assertHTTPStatus(t, http.MethodDelete, server.URL+"/missing", nil, nil, http.StatusNotFound, "NoSuchBucket")
	assertHTTPStatus(t, http.MethodPut, server.URL+"/missing?notification", strings.NewReader("<"), nil, http.StatusBadRequest, "MalformedXML")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/missing?notification", nil, nil, http.StatusNotFound, "NoSuchBucket")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/missing?notification", nil, nil, http.StatusMethodNotAllowed, "MethodNotAllowed")

	assertHTTPStatus(t, http.MethodPut, server.URL+"/assets", nil, nil, http.StatusOK, "")
	assertHTTPStatus(t, http.MethodPut, server.URL+"/assets?notification", strings.NewReader(`<NotificationConfiguration><QueueConfiguration><Queue>arn:aws:sqs:us-east-1:000000000000:missing</Queue><Event>s3:ObjectCreated:*</Event></QueueConfiguration></NotificationConfiguration>`), nil, http.StatusBadRequest, "InvalidArgument")
	assertHTTPStatus(t, http.MethodPut, server.URL+"/assets/key?partNumber=0&uploadId=missing", nil, nil, http.StatusBadRequest, "InvalidArgument")
	assertHTTPStatus(t, http.MethodPut, server.URL+"/assets/key?partNumber=1&uploadId=missing", strings.NewReader("body"), nil, http.StatusNotFound, "NoSuchUpload")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/assets/key?part-number-marker=-1&uploadId=missing", nil, nil, http.StatusBadRequest, "InvalidArgument")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/assets/key?max-parts=1001&uploadId=missing", nil, nil, http.StatusBadRequest, "InvalidArgument")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/assets/key?uploadId=missing", nil, nil, http.StatusNotFound, "NoSuchUpload")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/assets/key?uploadId=missing", strings.NewReader("<"), nil, http.StatusBadRequest, "MalformedXML")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/assets/key?uploadId=missing", strings.NewReader("<CompleteMultipartUpload/>"), nil, http.StatusNotFound, "NoSuchUpload")
	assertHTTPStatus(t, http.MethodDelete, server.URL+"/assets/key?uploadId=missing", nil, nil, http.StatusNotFound, "NoSuchUpload")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/assets?max-uploads=invalid&uploads", nil, nil, http.StatusBadRequest, "InvalidArgument")

	assertHTTPStatus(t, http.MethodPut, server.URL+"/assets/copy", nil, map[string]string{"x-amz-copy-source": "invalid"}, http.StatusBadRequest, "InvalidArgument")
	assertHTTPStatus(t, http.MethodPut, server.URL+"/assets/copy", nil, map[string]string{"x-amz-copy-source": "/missing/key"}, http.StatusNotFound, "NoSuchBucket")
	assertHTTPStatus(t, http.MethodPut, server.URL+"/assets/source", strings.NewReader("data"), map[string]string{"Content-Type": "text/plain", "x-amz-meta-env": "test"}, http.StatusOK, "")
	assertHTTPStatus(t, http.MethodPut, server.URL+"/missing/copy", nil, map[string]string{"x-amz-copy-source": "/assets/source"}, http.StatusNotFound, "NoSuchBucket")
	assertHTTPStatus(t, http.MethodPut, server.URL+"/assets/copy", nil, map[string]string{
		"x-amz-copy-source":        "/assets/source",
		"x-amz-metadata-directive": "REPLACE",
		"Content-Type":             "application/json",
		"x-amz-meta-env":           "replaced",
	}, http.StatusOK, "CopyObjectResult")

	if bucket, key := s3Resource(httptest.NewRequest(http.MethodGet, "http://photos.localhost/path%20one", nil)); bucket != "photos" || key != "path one" {
		t.Fatalf("unexpected virtual-host resource: bucket=%q key=%q", bucket, key)
	}
	if _, _, err := parseCopySource("%zz"); err == nil {
		t.Fatal("malformed escaped copy source should fail")
	}
	if bucket, key, err := parseCopySource("/assets/path%20one?versionId=1"); err != nil || bucket != "assets" || key != "path one" {
		t.Fatalf("unexpected parsed copy source: bucket=%q key=%q err=%v", bucket, key, err)
	}
	metadata := s3Metadata(http.Header{"X-Amz-Meta-Env": []string{"one", "two"}, "Other": []string{"ignored"}})
	if metadata["env"] != "one,two" || len(metadata) != 1 {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
}

func TestPubSubServerValidationPaginationAndErrors(t *testing.T) {
	store := openServerTestStore(t)
	service := &pubSubServer{store: store}
	ctx := context.Background()
	topicA := "projects/test/topics/a"
	topicB := "projects/test/topics/b"
	subA := "projects/test/subscriptions/a"
	subB := "projects/test/subscriptions/b"

	assertGRPCCode(t, grpcCallError(service.CreateTopic(ctx, &pubsubpb.Topic{})), codes.InvalidArgument)
	if _, err := service.CreateTopic(ctx, &pubsubpb.Topic{Name: topicA, Labels: map[string]string{"env": "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTopic(ctx, &pubsubpb.Topic{Name: topicB}); err != nil {
		t.Fatal(err)
	}
	assertGRPCCode(t, grpcCallError(service.CreateTopic(ctx, &pubsubpb.Topic{Name: topicA})), codes.AlreadyExists)
	assertGRPCCode(t, grpcCallError(service.GetTopic(ctx, &pubsubpb.GetTopicRequest{Topic: "missing"})), codes.NotFound)
	topics, err := service.ListTopics(ctx, &pubsubpb.ListTopicsRequest{Project: "projects/test", PageSize: 1})
	if err != nil || len(topics.GetTopics()) != 1 || topics.GetNextPageToken() == "" {
		t.Fatalf("unexpected topic page: response=%v err=%v", topics, err)
	}
	nextTopics, err := service.ListTopics(ctx, &pubsubpb.ListTopicsRequest{Project: "projects/test", PageSize: 1, PageToken: topics.GetNextPageToken()})
	if err != nil || len(nextTopics.GetTopics()) != 1 {
		t.Fatalf("unexpected next topic page: response=%v err=%v", nextTopics, err)
	}
	assertGRPCCode(t, grpcCallError(service.Publish(ctx, &pubsubpb.PublishRequest{Topic: "missing"})), codes.NotFound)
	assertGRPCCode(t, grpcCallError(service.ListTopicSubscriptions(ctx, &pubsubpb.ListTopicSubscriptionsRequest{Topic: "missing"})), codes.NotFound)

	assertGRPCCode(t, grpcCallError(service.CreateSubscription(ctx, &pubsubpb.Subscription{})), codes.InvalidArgument)
	assertGRPCCode(t, grpcCallError(service.CreateSubscription(ctx, &pubsubpb.Subscription{Name: subA, Topic: "missing"})), codes.NotFound)
	if _, err := service.CreateSubscription(ctx, &pubsubpb.Subscription{Name: subA, Topic: topicA, AckDeadlineSeconds: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSubscription(ctx, &pubsubpb.Subscription{Name: subB, Topic: topicA, AckDeadlineSeconds: 10}); err != nil {
		t.Fatal(err)
	}
	assertGRPCCode(t, grpcCallError(service.CreateSubscription(ctx, &pubsubpb.Subscription{Name: subA, Topic: topicA})), codes.AlreadyExists)
	assertGRPCCode(t, grpcCallError(service.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{Subscription: "missing"})), codes.NotFound)
	subscriptions, err := service.ListSubscriptions(ctx, &pubsubpb.ListSubscriptionsRequest{Project: "projects/test", PageSize: 1})
	if err != nil || len(subscriptions.GetSubscriptions()) != 1 || subscriptions.GetNextPageToken() == "" {
		t.Fatalf("unexpected subscription page: response=%v err=%v", subscriptions, err)
	}
	topicSubscriptions, err := service.ListTopicSubscriptions(ctx, &pubsubpb.ListTopicSubscriptionsRequest{Topic: topicA, PageSize: 1})
	if err != nil || len(topicSubscriptions.GetSubscriptions()) != 1 || topicSubscriptions.GetNextPageToken() == "" {
		t.Fatalf("unexpected topic-subscription page: response=%v err=%v", topicSubscriptions, err)
	}

	assertGRPCCode(t, grpcCallError(service.UpdateSubscription(ctx, &pubsubpb.UpdateSubscriptionRequest{})), codes.InvalidArgument)
	assertGRPCCode(t, grpcCallError(service.UpdateSubscription(ctx, &pubsubpb.UpdateSubscriptionRequest{
		Subscription: &pubsubpb.Subscription{Name: subA},
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"unsupported"}},
	})), codes.InvalidArgument)
	assertGRPCCode(t, grpcCallError(service.UpdateSubscription(ctx, &pubsubpb.UpdateSubscriptionRequest{
		Subscription: &pubsubpb.Subscription{Name: subA, DeadLetterPolicy: &pubsubpb.DeadLetterPolicy{DeadLetterTopic: topicB, MaxDeliveryAttempts: 4}},
		UpdateMask:   &fieldmaskpb.FieldMask{Paths: []string{"dead_letter_policy"}},
	})), codes.InvalidArgument)
	assertGRPCCode(t, grpcCallError(service.UpdateSubscription(ctx, &pubsubpb.UpdateSubscriptionRequest{
		Subscription: &pubsubpb.Subscription{Name: "missing"},
	})), codes.NotFound)
	updated, err := service.UpdateSubscription(ctx, &pubsubpb.UpdateSubscriptionRequest{
		Subscription: &pubsubpb.Subscription{Name: subA, AckDeadlineSeconds: 20, Labels: map[string]string{"updated": "true"}},
	})
	if err != nil || updated.GetAckDeadlineSeconds() != 20 || updated.GetLabels()["updated"] != "true" {
		t.Fatalf("unexpected subscription update: response=%v err=%v", updated, err)
	}

	assertGRPCCode(t, grpcCallError(service.Pull(ctx, &pubsubpb.PullRequest{Subscription: subA})), codes.InvalidArgument)
	assertGRPCCode(t, grpcCallError(service.Pull(ctx, &pubsubpb.PullRequest{Subscription: "missing", MaxMessages: 1})), codes.NotFound)
	assertGRPCCode(t, grpcCallError(service.Acknowledge(ctx, &pubsubpb.AcknowledgeRequest{Subscription: "missing"})), codes.NotFound)
	assertGRPCCode(t, grpcCallError(service.ModifyAckDeadline(ctx, &pubsubpb.ModifyAckDeadlineRequest{Subscription: "missing"})), codes.NotFound)
	assertGRPCCode(t, grpcCallError(service.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: "missing"})), codes.NotFound)
	assertGRPCCode(t, grpcCallError(service.DeleteTopic(ctx, &pubsubpb.DeleteTopicRequest{Topic: "missing"})), codes.NotFound)

	if status.Code(pubSubStateError(state.ErrPubSubTopicNotFound)) != codes.NotFound {
		t.Fatal("Pub/Sub state not-found error should map to NotFound")
	}
	if status.Code(pubSubStateError(errors.New("broken"))) != codes.Internal {
		t.Fatal("unexpected Pub/Sub state error should map to Internal")
	}
	if got := receivedProtos([]state.PubSubMessage{{MessageID: "one", DeliveryAttempt: 3}}, false); got[0].GetDeliveryAttempt() != 0 {
		t.Fatalf("delivery attempt should be omitted without a dead-letter policy: %+v", got)
	}
	if got := receivedProtos([]state.PubSubMessage{{MessageID: "one", DeliveryAttempt: 3}}, true); got[0].GetDeliveryAttempt() != 3 {
		t.Fatalf("delivery attempt should be preserved with a dead-letter policy: %+v", got)
	}

	grpcServer := NewPubSubGRPCServer(store)
	grpcServer.Stop()
}

func openServerTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func assertHTTPStatus(t *testing.T, method, target string, body io.Reader, headers map[string]string, wantStatus int, wantBody string) {
	t.Helper()
	response := doHTTPRequest(t, method, target, body, headers)
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != wantStatus || (wantBody != "" && !bytes.Contains(responseBody, []byte(wantBody))) {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, target, response.StatusCode, wantStatus, responseBody)
	}
}

func doHTTPRequest(t *testing.T, method, target string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func grpcCallError[T any](_ T, err error) error {
	return err
}

func assertGRPCCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("gRPC code=%s want=%s err=%v", status.Code(err), want, err)
	}
}

func TestQueueAndExternalURLHelpers(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://localhost/queue", nil)
	request.Header.Set("X-Forwarded-Proto", "https, http")
	if got := queueURL(request, "jobs"); got != "https://localhost/000000000000/jobs" {
		t.Fatalf("unexpected queue URL: %q", got)
	}
	if got := queueName("https://localhost/000000000000/jobs"); got != "jobs" {
		t.Fatalf("unexpected queue name: %q", got)
	}
	if got := queueName("%zz"); got != "" {
		t.Fatalf("malformed queue URL should not resolve a queue name: %q", got)
	}
	if got := selectAttributes(map[string]string{"A": "1", "B": "2"}, nil); len(got) != 0 {
		t.Fatalf("empty attribute selection should be empty: %+v", got)
	}
	if got := selectAttributes(map[string]string{"A": "1", "B": "2"}, []string{"A", "missing"}); len(got) != 1 || got["A"] != "1" {
		t.Fatalf("unexpected attribute selection: %+v", got)
	}
	if got := selectAttributes(map[string]string{"A": "1"}, []string{"All"}); got["A"] != "1" {
		t.Fatalf("All should return attributes: %+v", got)
	}
	delay := 3
	if value, specified := sqsDelay(nil); value != -1 || specified {
		t.Fatalf("nil delay should be unspecified: value=%d specified=%v", value, specified)
	}
	if value, specified := sqsDelay(&delay); value != 3 || !specified {
		t.Fatalf("explicit delay should be preserved: value=%d specified=%v", value, specified)
	}
}
