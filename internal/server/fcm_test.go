package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFCMCapturesMessagesAndReturnsDeterministicErrors(t *testing.T) {
	server := newTestServer(t)
	requestBody := []byte(`{"message":{"token":"device-token","notification":{"title":"hello"},"data":{"reservationId":"42"}}}`)
	response, err := http.Post(server.URL+"/v1/projects/fcp-local/messages:send", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected send status %d: %s", response.StatusCode, body)
	}
	var sent map[string]string
	if err := json.NewDecoder(response.Body).Decode(&sent); err != nil {
		t.Fatal(err)
	}
	if sent["name"] == "" {
		t.Fatal("FCM message name is empty")
	}

	listed, err := http.Get(server.URL + "/_fcp/fcm/messages?project=fcp-local")
	if err != nil {
		t.Fatal(err)
	}
	defer listed.Body.Close()
	var capture struct {
		Messages []struct {
			Project string          `json:"project"`
			Message json.RawMessage `json:"message"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(listed.Body).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Messages) != 1 || capture.Messages[0].Project != "fcp-local" || !bytes.Contains(capture.Messages[0].Message, []byte(`"reservationId":"42"`)) {
		t.Fatalf("unexpected capture: %+v", capture)
	}

	errorResponse, err := http.Post(server.URL+"/v1/projects/fcp-local/messages:send", "application/json", bytes.NewBufferString(`{"message":{"token":"fcp-error-unregistered-1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer errorResponse.Body.Close()
	if errorResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("expected deterministic unregistered token error, got %d", errorResponse.StatusCode)
	}
}

func TestVertexModelList(t *testing.T) {
	server := newTestServer(t)
	response, err := http.Get(server.URL + "/v1/projects/fcp-local/locations/asia-northeast3/publishers/google/models")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("gemini-2.5-flash")) {
		t.Fatalf("unexpected model list: %s", body)
	}
}

func TestVertexGenerateContentCapturesMetadataWithoutPrompt(t *testing.T) {
	server := newTestServer(t)
	requestBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"sensitive-local-prompt"}]}],"tools":[{"functionDeclarations":[]}]}`)
	response, err := http.Post(server.URL+"/v1beta/models/gemini-2.5-flash:generateContent?key=fcp-local", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("unexpected generateContent status=%d body=%s", response.StatusCode, body)
	}
	var generated struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(response.Body).Decode(&generated); err != nil {
		t.Fatal(err)
	}
	if len(generated.Candidates) != 1 || generated.Candidates[0].Content.Parts[0].Text != "FCP local generated response" {
		t.Fatalf("unexpected generation response: %+v", generated)
	}

	vertexResponse, err := http.Post(server.URL+"/v1beta1/projects/fcp-local/locations/asia-northeast3/publishers/google/models/gemini-2.5-pro:generateContent", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	vertexResponse.Body.Close()
	if vertexResponse.StatusCode != http.StatusOK {
		t.Fatalf("unexpected Vertex generation status=%d", vertexResponse.StatusCode)
	}

	listed, err := http.Get(server.URL + "/_fcp/vertex/generations?project=fcp-local")
	if err != nil {
		t.Fatal(err)
	}
	defer listed.Body.Close()
	metadata, err := io.ReadAll(listed.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(metadata, []byte(`"model":"gemini-2.5-flash"`)) || !bytes.Contains(metadata, []byte(`"location":"asia-northeast3"`)) {
		t.Fatalf("generation metadata missing: %s", metadata)
	}
	if bytes.Contains(metadata, []byte("sensitive-local-prompt")) || bytes.Contains(metadata, []byte("FCP local generated response")) {
		t.Fatalf("generation metadata exposed prompt or response: %s", metadata)
	}
}

func TestVertexStreamAndDeterministicError(t *testing.T) {
	server := newTestServer(t)
	requestBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") || !bytes.Contains(body, []byte("data: {")) {
		t.Fatalf("unexpected streaming response status=%d content-type=%q body=%s", response.StatusCode, response.Header.Get("Content-Type"), body)
	}

	errorResponse, err := http.Post(server.URL+"/v1beta/models/fcp-error-rate-limit:generateContent", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	defer errorResponse.Body.Close()
	if errorResponse.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected deterministic rate limit, got %d", errorResponse.StatusCode)
	}
}
