package server

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devy1540/fcp/internal/state"
	rpccode "google.golang.org/genproto/googleapis/rpc/code"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSQSErrorAndMutationBranches(t *testing.T) {
	server := newTestServer(t)
	assertHTTPStatus(t, http.MethodGet, server.URL+"/", nil, map[string]string{"X-Amz-Target": "AmazonSQS.ListQueues"}, http.StatusMethodNotAllowed, "UnsupportedOperation")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/", strings.NewReader("{"), map[string]string{"X-Amz-Target": "AmazonSQS.ListQueues"}, http.StatusBadRequest, "InvalidRequest")

	assertSQSStatus(t, server.URL, "CreateQueue", map[string]any{
		"QueueName": "invalid.fifo", "Attributes": map[string]string{"FifoQueue": "false"},
	}, http.StatusBadRequest, "InvalidAttributeValue")
	sqsCall(t, server.URL, "CreateQueue", map[string]any{"QueueName": "jobs"})
	queueURL := server.URL + "/000000000000/jobs"
	assertSQSStatus(t, server.URL, "GetQueueUrl", map[string]any{"QueueName": "missing"}, http.StatusBadRequest, "AWS.SimpleQueueService.NonExistentQueue")
	sqsCall(t, server.URL, "GetQueueUrl", map[string]any{"QueueName": "jobs"})
	sqsCall(t, server.URL, "ListQueues", map[string]any{"QueueNamePrefix": "jo"})
	assertSQSStatus(t, server.URL, "DeleteQueue", map[string]any{"QueueUrl": server.URL + "/missing"}, http.StatusBadRequest, "AWS.SimpleQueueService.NonExistentQueue")
	assertSQSStatus(t, server.URL, "PurgeQueue", map[string]any{"QueueUrl": server.URL + "/missing"}, http.StatusBadRequest, "AWS.SimpleQueueService.NonExistentQueue")
	assertSQSStatus(t, server.URL, "ReceiveMessage", map[string]any{"QueueUrl": server.URL + "/missing", "MaxNumberOfMessages": 1}, http.StatusBadRequest, "AWS.SimpleQueueService.NonExistentQueue")
	assertSQSStatus(t, server.URL, "DeleteMessage", map[string]any{"QueueUrl": server.URL + "/missing", "ReceiptHandle": "receipt"}, http.StatusBadRequest, "AWS.SimpleQueueService.NonExistentQueue")
	assertSQSStatus(t, server.URL, "ChangeMessageVisibility", map[string]any{"QueueUrl": server.URL + "/missing", "ReceiptHandle": "receipt"}, http.StatusBadRequest, "AWS.SimpleQueueService.NonExistentQueue")
	assertSQSStatus(t, server.URL, "GetQueueAttributes", map[string]any{"QueueUrl": server.URL + "/missing"}, http.StatusBadRequest, "AWS.SimpleQueueService.NonExistentQueue")
	assertSQSStatus(t, server.URL, "SetQueueAttributes", map[string]any{"QueueUrl": server.URL + "/missing"}, http.StatusBadRequest, "AWS.SimpleQueueService.NonExistentQueue")

	assertSQSStatus(t, server.URL, "SendMessage", map[string]any{"QueueUrl": queueURL, "MessageBody": "body", "DelaySeconds": 901}, http.StatusBadRequest, "InvalidParameterValue")
	assertSQSStatus(t, server.URL, "DeleteMessage", map[string]any{"QueueUrl": queueURL, "ReceiptHandle": "invalid"}, http.StatusBadRequest, "ReceiptHandleIsInvalid")
	assertSQSStatus(t, server.URL, "ChangeMessageVisibility", map[string]any{"QueueUrl": queueURL, "ReceiptHandle": "invalid", "VisibilityTimeout": 1}, http.StatusBadRequest, "ReceiptHandleIsInvalid")
	assertSQSStatus(t, server.URL, "SetQueueAttributes", map[string]any{"QueueUrl": queueURL, "Attributes": map[string]string{"FifoQueue": "true"}}, http.StatusBadRequest, "InvalidAttributeValue")
	sqsCall(t, server.URL, "PurgeQueue", map[string]any{"QueueUrl": queueURL})

	binary := []byte("binary")
	if digest := sqsMessageAttributesMD5(map[string]state.MessageAttribute{"data": {DataType: "Binary", BinaryValue: binary}}); digest == "" {
		t.Fatal("binary message attributes should produce an MD5")
	}
	for _, test := range []struct {
		err         error
		code        string
		senderFault bool
	}{
		{err: state.ErrQueueNotFound, code: "AWS.SimpleQueueService.NonExistentQueue", senderFault: true},
		{err: state.ErrMissingMessageParameter, code: "MissingParameter", senderFault: true},
		{err: state.ErrInvalidMessageParameter, code: "InvalidParameterValue", senderFault: true},
		{err: errors.New("broken"), code: "InternalError", senderFault: false},
	} {
		code, senderFault := sqsSendError(test.err)
		if code != test.code || senderFault != test.senderFault {
			t.Fatalf("error=%v code=%q fault=%v", test.err, code, senderFault)
		}
	}
}

func TestAdminGoogleRESTAndFCMErrorBranches(t *testing.T) {
	server := newTestServer(t)
	assertHTTPStatus(t, http.MethodGet, server.URL+"/_fcp/health", nil, nil, http.StatusOK, `"status":"ok"`)
	assertHTTPStatus(t, http.MethodPost, server.URL+"/_fcp/reset", nil, nil, http.StatusNoContent, "")
	assertHTTPStatus(t, http.MethodDelete, server.URL+"/_fcp/fcm/messages", nil, nil, http.StatusNoContent, "")
	assertHTTPStatus(t, http.MethodDelete, server.URL+"/_fcp/vertex/generations", nil, nil, http.StatusNoContent, "")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/_fcp/missing", nil, nil, http.StatusNotFound, "")

	assertHTTPStatus(t, http.MethodPost, server.URL+"/computeMetadata/v1/project/project-id", nil, map[string]string{"Metadata-Flavor": "Google"}, http.StatusMethodNotAllowed, "")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/computeMetadata/v1/instance/service-accounts/default/identity", nil, map[string]string{"Metadata-Flavor": "Google"}, http.StatusBadRequest, "audience is required")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/computeMetadata/v1/missing", nil, map[string]string{"Metadata-Flavor": "Google"}, http.StatusNotFound, "")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/oauth2/v3/certs", nil, nil, http.StatusMethodNotAllowed, "")
	assertHTTPStatus(t, http.MethodPost, server.URL+"/v1/projects/test/secrets/missing/versions/latest:access", nil, nil, http.StatusMethodNotAllowed, "")
	assertHTTPStatus(t, http.MethodGet, server.URL+"/v1/projects/test/secrets/missing/versions/latest:access", nil, nil, http.StatusNotFound, "NOT_FOUND")

	kmsPath := server.URL + "/v1/projects/test/locations/global/keyRings/test/cryptoKeys/missing"
	assertHTTPStatus(t, http.MethodGet, kmsPath+":encrypt", nil, nil, http.StatusMethodNotAllowed, "")
	assertHTTPStatus(t, http.MethodPost, kmsPath+":encrypt", strings.NewReader("{"), nil, http.StatusBadRequest, "invalid JSON")
	assertHTTPStatus(t, http.MethodPost, kmsPath+":encrypt", strings.NewReader(`{"additionalAuthenticatedData":"%"}`), nil, http.StatusBadRequest, "must be base64")
	assertHTTPStatus(t, http.MethodPost, kmsPath+":encrypt", strings.NewReader(`{"plaintext":"%"}`), nil, http.StatusBadRequest, "plaintext must be base64")
	assertHTTPStatus(t, http.MethodPost, kmsPath+":encrypt", strings.NewReader(`{"plaintext":"aGVsbG8="}`), nil, http.StatusNotFound, "NOT_FOUND")
	assertHTTPStatus(t, http.MethodPost, kmsPath+":decrypt", strings.NewReader(`{"ciphertext":"%"}`), nil, http.StatusBadRequest, "ciphertext must be base64")
	assertHTTPStatus(t, http.MethodPost, kmsPath+":decrypt", strings.NewReader(`{"ciphertext":"aGVsbG8="}`), nil, http.StatusNotFound, "NOT_FOUND")

	fcmPath := server.URL + "/v1/projects/test/messages:send"
	assertHTTPStatus(t, http.MethodGet, fcmPath, nil, nil, http.StatusNotFound, "")
	assertHTTPStatus(t, http.MethodPost, fcmPath, strings.NewReader("{"), nil, http.StatusBadRequest, "message is required")
	assertHTTPStatus(t, http.MethodPost, fcmPath, strings.NewReader(`{"message":"text"}`), nil, http.StatusBadRequest, "message must be an object")
	assertHTTPStatus(t, http.MethodPost, fcmPath, strings.NewReader(`{"message":{}}`), nil, http.StatusBadRequest, "message target is required")

	if got := (&dashboardRequestError{status: http.StatusBadRequest, message: "bad request"}).Error(); got != "bad request" {
		t.Fatalf("unexpected dashboard request error: %q", got)
	}
}

func TestGoogleGRPCErrorMappingsAndBase64(t *testing.T) {
	for _, test := range []struct {
		code       codes.Code
		httpStatus int
	}{
		{code: codes.InvalidArgument, httpStatus: http.StatusBadRequest},
		{code: codes.FailedPrecondition, httpStatus: http.StatusBadRequest},
		{code: codes.NotFound, httpStatus: http.StatusNotFound},
		{code: codes.AlreadyExists, httpStatus: http.StatusConflict},
		{code: codes.PermissionDenied, httpStatus: http.StatusForbidden},
		{code: codes.Unauthenticated, httpStatus: http.StatusUnauthorized},
		{code: codes.Unimplemented, httpStatus: http.StatusNotImplemented},
		{code: codes.Internal, httpStatus: http.StatusInternalServerError},
	} {
		recorder := httptest.NewRecorder()
		writeGoogleGRPCError(recorder, status.Error(test.code, "failure"))
		if recorder.Code != test.httpStatus || !bytes.Contains(recorder.Body.Bytes(), []byte(rpccode.Code(test.code).String())) {
			t.Fatalf("code=%s status=%d body=%s", test.code, recorder.Code, recorder.Body)
		}
	}
	if decoded, err := decodeGoogleBase64("aGVsbG8"); err != nil || string(decoded) != "hello" {
		t.Fatalf("raw base64 decode failed: decoded=%q err=%v", decoded, err)
	}
	if decoded, err := decodeGoogleBase64(""); err != nil || decoded != nil {
		t.Fatalf("empty base64 should decode to nil: decoded=%v err=%v", decoded, err)
	}
}

func assertSQSStatus(t *testing.T, endpoint, operation string, body any, wantStatus int, wantCode string) {
	t.Helper()
	response := sqsCallResponse(t, endpoint, operation, body)
	defer response.Body.Close()
	if response.StatusCode != wantStatus || response.Header.Get("x-amzn-ErrorType") != wantCode {
		t.Fatalf("SQS %s status=%d want=%d code=%q want=%q", operation, response.StatusCode, wantStatus, response.Header.Get("x-amzn-ErrorType"), wantCode)
	}
}
