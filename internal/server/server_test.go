package server

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/devy1540/fcp/internal/state"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(store))
	t.Cleanup(server.Close)
	return server
}

func TestSQSMessageAttributesMD5MatchesAWSSDK(t *testing.T) {
	attributes := map[string]state.MessageAttribute{
		"contentType": {DataType: "String", StringValue: "application/json"},
	}
	if got, want := sqsMessageAttributesMD5(attributes), "6ed5f16969b625c8d900cbd5da557e9e"; got != want {
		t.Fatalf("message attribute MD5 = %q, want %q", got, want)
	}
}

func TestS3AndSQSWithAWSCLI(t *testing.T) {
	if _, err := exec.LookPath("aws"); err != nil {
		t.Skip("aws CLI not installed")
	}
	server := newTestServer(t)
	env := []string{"AWS_ACCESS_KEY_ID=test", "AWS_SECRET_ACCESS_KEY=test", "AWS_REGION=us-east-1", "AWS_MAX_ATTEMPTS=1", "AWS_PAGER="}
	runAWS := func(args ...string) string {
		t.Helper()
		base := []string{"--endpoint-url", server.URL}
		cmd := exec.Command("aws", append(base, args...)...)
		cmd.Env = append(cmd.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("aws %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	runAWS("s3api", "create-bucket", "--bucket", "assets")
	input := t.TempDir() + "/hello.txt"
	if err := os.WriteFile(input, []byte("hello fcp"), 0o600); err != nil {
		t.Fatal(err)
	}
	putOut := runAWS("s3api", "put-object", "--bucket", "assets", "--key", "docs/hello.txt", "--body", input, "--metadata", "env=test")
	if !strings.Contains(putOut, "ETag") {
		t.Fatalf("unexpected put-object output: %s", putOut)
	}
	listOut := runAWS("s3api", "list-objects-v2", "--bucket", "assets", "--prefix", "docs/")
	if !strings.Contains(listOut, "docs/hello.txt") {
		t.Fatalf("unexpected list output: %s", listOut)
	}

	createOut := runAWS("sqs", "create-queue", "--queue-name", "jobs", "--attributes", "VisibilityTimeout=1")
	var created struct {
		QueueURL string `json:"QueueUrl"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil || created.QueueURL == "" {
		t.Fatalf("unexpected create queue output: %s (%v)", createOut, err)
	}
	runAWS("sqs", "send-message", "--queue-url", created.QueueURL, "--message-body", "hello")
	receiveOut := runAWS("sqs", "receive-message", "--queue-url", created.QueueURL, "--attribute-names", "All")
	if !strings.Contains(receiveOut, `"Body": "hello"`) {
		t.Fatalf("unexpected receive output: %s", receiveOut)
	}
}

func TestNativeBucketNotificationConfiguration(t *testing.T) {
	server := newTestServer(t)
	sqsCall(t, server.URL, "CreateQueue", map[string]any{"QueueName": "events"})
	s3Call(t, http.MethodPut, server.URL+"/uploads", nil, nil)
	config := `<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><QueueConfiguration><Id>images</Id><Queue>arn:aws:sqs:us-east-1:000000000000:events</Queue><Event>s3:ObjectCreated:*</Event><Filter><S3Key><FilterRule><Name>prefix</Name><Value>images/</Value></FilterRule></S3Key></Filter></QueueConfiguration></NotificationConfiguration>`
	s3Call(t, http.MethodPut, server.URL+"/uploads?notification", strings.NewReader(config), nil)
	s3Call(t, http.MethodPut, server.URL+"/uploads/images/cat.jpg", strings.NewReader("image"), map[string]string{"Content-Type": "image/jpeg"})
	response := sqsCall(t, server.URL, "ReceiveMessage", map[string]any{"QueueUrl": server.URL + "/000000000000/events", "MaxNumberOfMessages": 1})
	var received struct {
		Messages []struct {
			Body string `json:"Body"`
		} `json:"Messages"`
	}
	if err := json.Unmarshal(response, &received); err != nil {
		t.Fatal(err)
	}
	if len(received.Messages) != 1 || !strings.Contains(received.Messages[0].Body, `"key":"images/cat.jpg"`) {
		t.Fatalf("unexpected notification: %s", response)
	}
}

func TestS3RangeAndCopyObject(t *testing.T) {
	server := newTestServer(t)
	s3Call(t, http.MethodPut, server.URL+"/source", nil, nil)
	s3Call(t, http.MethodPut, server.URL+"/destination", nil, nil)
	s3Call(t, http.MethodPut, server.URL+"/source/docs/hello.txt", strings.NewReader("hello fcp"), map[string]string{
		"Content-Type":      "text/plain",
		"x-amz-meta-source": "unit-test",
	})

	for _, test := range []struct {
		name         string
		rangeHeader  string
		contentRange string
		body         string
	}{
		{name: "bounded", rangeHeader: "bytes=1-3", contentRange: "bytes 1-3/9", body: "ell"},
		{name: "open", rangeHeader: "bytes=6-", contentRange: "bytes 6-8/9", body: "fcp"},
		{name: "suffix", rangeHeader: "bytes=-3", contentRange: "bytes 6-8/9", body: "fcp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rangeRequest, err := http.NewRequest(http.MethodGet, server.URL+"/source/docs/hello.txt", nil)
			if err != nil {
				t.Fatal(err)
			}
			rangeRequest.Header.Set("Range", test.rangeHeader)
			rangeResponse, err := http.DefaultClient.Do(rangeRequest)
			if err != nil {
				t.Fatal(err)
			}
			rangeBody, err := io.ReadAll(rangeResponse.Body)
			rangeResponse.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if rangeResponse.StatusCode != http.StatusPartialContent || rangeResponse.Header.Get("Content-Range") != test.contentRange || string(rangeBody) != test.body {
				t.Fatalf("unexpected range response: status=%d content-range=%q body=%q", rangeResponse.StatusCode, rangeResponse.Header.Get("Content-Range"), rangeBody)
			}
		})
	}

	copyRequest, err := http.NewRequest(http.MethodPut, server.URL+"/destination/copied.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	copyRequest.Header.Set("x-amz-copy-source", "/source/docs%2Fhello.txt")
	copyResponse, err := http.DefaultClient.Do(copyRequest)
	if err != nil {
		t.Fatal(err)
	}
	copyBody, err := io.ReadAll(copyResponse.Body)
	copyResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if copyResponse.StatusCode != http.StatusOK || !bytes.Contains(copyBody, []byte("<CopyObjectResult")) {
		t.Fatalf("unexpected copy response: status=%d body=%s", copyResponse.StatusCode, copyBody)
	}

	copiedRequest, err := http.NewRequest(http.MethodGet, server.URL+"/destination/copied.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	copiedResponse, err := http.DefaultClient.Do(copiedRequest)
	if err != nil {
		t.Fatal(err)
	}
	copiedBody, err := io.ReadAll(copiedResponse.Body)
	copiedResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(copiedBody) != "hello fcp" || copiedResponse.Header.Get("Content-Type") != "text/plain" || copiedResponse.Header.Get("x-amz-meta-source") != "unit-test" {
		t.Fatalf("unexpected copied object: body=%q content-type=%q metadata=%q", copiedBody, copiedResponse.Header.Get("Content-Type"), copiedResponse.Header.Get("x-amz-meta-source"))
	}
}

func TestS3MultipartUploadLifecycle(t *testing.T) {
	server := newTestServer(t)
	sqsCall(t, server.URL, "CreateQueue", map[string]any{"QueueName": "multipart-events"})
	s3Call(t, http.MethodPut, server.URL+"/assets", nil, nil)
	notification := `<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><QueueConfiguration><Id>multipart</Id><Queue>arn:aws:sqs:us-east-1:000000000000:multipart-events</Queue><Event>s3:ObjectCreated:CompleteMultipartUpload</Event></QueueConfiguration></NotificationConfiguration>`
	s3Call(t, http.MethodPut, server.URL+"/assets?notification", strings.NewReader(notification), nil)

	initiatedBody := s3Call(t, http.MethodPost, server.URL+"/assets/large.bin?uploads", nil, map[string]string{
		"Content-Type":      "application/octet-stream",
		"x-amz-meta-source": "unit-test",
	})
	var initiated initiateMultipartUploadResult
	if err := xml.Unmarshal(initiatedBody, &initiated); err != nil || initiated.UploadID == "" {
		t.Fatalf("unexpected initiate response: body=%s err=%v", initiatedBody, err)
	}

	partRequest, err := http.NewRequest(http.MethodPut, server.URL+"/assets/large.bin?partNumber=1&uploadId="+initiated.UploadID, strings.NewReader("multipart body"))
	if err != nil {
		t.Fatal(err)
	}
	partResponse, err := http.DefaultClient.Do(partRequest)
	if err != nil {
		t.Fatal(err)
	}
	partBody, _ := io.ReadAll(partResponse.Body)
	partResponse.Body.Close()
	partETag := partResponse.Header.Get("ETag")
	if partResponse.StatusCode != http.StatusOK || partETag == "" {
		t.Fatalf("unexpected upload part response: status=%d etag=%q body=%s", partResponse.StatusCode, partETag, partBody)
	}

	listedParts := s3Call(t, http.MethodGet, server.URL+"/assets/large.bin?uploadId="+initiated.UploadID, nil, nil)
	if !bytes.Contains(listedParts, []byte("<PartNumber>1</PartNumber>")) || !bytes.Contains(listedParts, []byte("<Size>14</Size>")) {
		t.Fatalf("unexpected list parts response: %s", listedParts)
	}
	listedUploads := s3Call(t, http.MethodGet, server.URL+"/assets?uploads&prefix=large", nil, nil)
	if !bytes.Contains(listedUploads, []byte(initiated.UploadID)) || !bytes.Contains(listedUploads, []byte("<Key>large.bin</Key>")) {
		t.Fatalf("unexpected list multipart uploads response: %s", listedUploads)
	}

	completeBody := `<CompleteMultipartUpload><Part><ETag>` + partETag + `</ETag><PartNumber>1</PartNumber></Part></CompleteMultipartUpload>`
	completed := s3Call(t, http.MethodPost, server.URL+"/assets/large.bin?uploadId="+initiated.UploadID, strings.NewReader(completeBody), nil)
	var completeResult completeMultipartUploadResult
	if err := xml.Unmarshal(completed, &completeResult); err != nil || !strings.HasSuffix(strings.Trim(completeResult.ETag, `"`), "-1") {
		t.Fatalf("unexpected complete response: body=%s result=%+v err=%v", completed, completeResult, err)
	}

	objectRequest, err := http.NewRequest(http.MethodGet, server.URL+"/assets/large.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	objectResponse, err := http.DefaultClient.Do(objectRequest)
	if err != nil {
		t.Fatal(err)
	}
	objectBody, _ := io.ReadAll(objectResponse.Body)
	objectResponse.Body.Close()
	if objectResponse.StatusCode != http.StatusOK || string(objectBody) != "multipart body" || objectResponse.Header.Get("x-amz-meta-source") != "unit-test" || !strings.HasSuffix(strings.Trim(objectResponse.Header.Get("ETag"), `"`), "-1") {
		t.Fatalf("unexpected multipart object: status=%d etag=%q metadata=%q body=%q", objectResponse.StatusCode, objectResponse.Header.Get("ETag"), objectResponse.Header.Get("x-amz-meta-source"), objectBody)
	}
	received := sqsCall(t, server.URL, "ReceiveMessage", map[string]any{"QueueUrl": server.URL + "/000000000000/multipart-events", "MaxNumberOfMessages": 1})
	if !bytes.Contains(received, []byte(`ObjectCreated:CompleteMultipartUpload`)) {
		t.Fatalf("multipart completion notification missing: %s", received)
	}

	abortBody := s3Call(t, http.MethodPost, server.URL+"/assets/aborted.bin?uploads", nil, nil)
	var aborted initiateMultipartUploadResult
	if err := xml.Unmarshal(abortBody, &aborted); err != nil || aborted.UploadID == "" {
		t.Fatalf("unexpected abort initiate response: body=%s err=%v", abortBody, err)
	}
	s3Call(t, http.MethodPut, server.URL+"/assets/aborted.bin?partNumber=1&uploadId="+aborted.UploadID, strings.NewReader("discarded"), nil)
	s3Call(t, http.MethodDelete, server.URL+"/assets/aborted.bin?uploadId="+aborted.UploadID, nil, nil)
	missingResponse, err := http.Get(server.URL + "/assets/aborted.bin?uploadId=" + aborted.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	missingBody, _ := io.ReadAll(missingResponse.Body)
	missingResponse.Body.Close()
	if missingResponse.StatusCode != http.StatusNotFound || !bytes.Contains(missingBody, []byte("<Code>NoSuchUpload</Code>")) {
		t.Fatalf("aborted upload remained: status=%d body=%s", missingResponse.StatusCode, missingBody)
	}
}

func sqsCall(t *testing.T, endpoint, operation string, body any) []byte {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, endpoint+"/", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Amz-Target", "AmazonSQS."+operation)
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	result, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("SQS %s returned %d: %s", operation, resp.StatusCode, result)
	}
	return result
}

func s3Call(t *testing.T, method, endpoint string, body io.Reader, headers map[string]string) []byte {
	t.Helper()
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	result, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("S3 %s returned %d: %s", endpoint, resp.StatusCode, result)
	}
	return result
}
