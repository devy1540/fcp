package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type fcmSendRequest struct {
	Message      json.RawMessage `json:"message"`
	ValidateOnly bool            `json:"validate_only,omitempty"`
}

func (s *Server) handleFCM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/messages:send") {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != "projects" || parts[2] == "" || parts[3] != "messages:send" {
		http.NotFound(w, r)
		return
	}
	project := parts[2]
	var request fcmSendRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&request); err != nil || len(request.Message) == 0 || string(request.Message) == "null" {
		writeGoogleAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "message is required")
		return
	}
	var target struct {
		Token     string `json:"token"`
		Topic     string `json:"topic"`
		Condition string `json:"condition"`
	}
	if err := json.Unmarshal(request.Message, &target); err != nil {
		writeGoogleAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "message must be an object")
		return
	}
	if target.Token == "" && target.Topic == "" && target.Condition == "" {
		writeGoogleAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "message target is required")
		return
	}
	if strings.HasPrefix(target.Token, "fcp-error-unregistered") {
		writeGoogleAPIError(w, http.StatusNotFound, "NOT_FOUND", "Requested entity was not found.")
		return
	}
	recorded, err := s.store.RecordFCMMessage(project, request.Message, request.ValidateOnly)
	if err != nil {
		writeGoogleAPIError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"name": recorded.Name})
}

func writeGoogleAPIError(w http.ResponseWriter, code int, status, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "status": status}})
}

func (s *Server) handleVertex(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && (strings.HasSuffix(r.URL.Path, "/publishers/google/models") || strings.TrimRight(r.URL.Path, "/") == "/v1beta/models") {
		s.handleVertexModelList(w, r)
		return
	}
	project, location, model, operation, ok := vertexGenerationTarget(r.URL.Path)
	if r.Method != http.MethodPost || !ok {
		http.NotFound(w, r)
		return
	}
	if project == "" {
		project = s.projectID
	}
	if location == "" {
		location = "global"
	}
	if model == "fcp-error-rate-limit" {
		writeGoogleAPIError(w, http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "FCP deterministic rate limit")
		return
	}
	if model == "fcp-error-unavailable" {
		writeGoogleAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "FCP deterministic unavailable response")
		return
	}
	var request struct {
		Contents []vertexContent   `json:"contents"`
		System   vertexContent     `json:"systemInstruction"`
		Tools    []json.RawMessage `json:"tools"`
		Config   struct {
			ResponseMIMEType string `json:"responseMimeType"`
		} `json:"generationConfig"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 5<<20))
	if err := decoder.Decode(&request); err != nil {
		writeGoogleAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid generateContent request")
		return
	}
	if len(request.Contents) == 0 {
		writeGoogleAPIError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "contents is required")
		return
	}
	inputCharacters := vertexContentCharacters(request.System)
	for _, content := range request.Contents {
		inputCharacters += vertexContentCharacters(content)
	}
	recorded, err := s.store.RecordVertexGeneration(project, location, model, operation, inputCharacters, len(request.Tools))
	if err != nil {
		writeGoogleAPIError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	responseText := "FCP local generated response"
	if strings.EqualFold(request.Config.ResponseMIMEType, "application/json") {
		responseText = `{}`
	}
	response := vertexGenerateResponse(recorded.Name, model, responseText, inputCharacters)
	if operation == "streamGenerateContent" {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", mustJSON(response))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

type vertexContent struct {
	Role  string `json:"role"`
	Parts []struct {
		Text string `json:"text"`
	} `json:"parts"`
}

func (s *Server) handleVertexModelList(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{
		{"name": "publishers/google/models/gemini-2.5-flash", "displayName": "Gemini 2.5 Flash", "supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"}},
		{"name": "publishers/google/models/gemini-2.5-pro", "displayName": "Gemini 2.5 Pro", "supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"}},
	}
	if strings.TrimRight(r.URL.Path, "/") == "/v1beta/models" {
		models = []map[string]any{
			{"name": "models/gemini-2.5-flash", "displayName": "Gemini 2.5 Flash", "supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"}},
			{"name": "models/gemini-2.5-pro", "displayName": "Gemini 2.5 Pro", "supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"}},
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"publisherModels": models, "models": models})
}

func vertexGenerationTarget(path string) (project, location, model, operation string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 3 && (parts[0] == "v1beta" || parts[0] == "v1") && parts[1] == "models" {
		model, operation, ok = splitVertexModelOperation(parts[2])
		return "", "", model, operation, ok
	}
	if len(parts) == 9 && (parts[0] == "v1beta1" || parts[0] == "v1") && parts[1] == "projects" && parts[3] == "locations" && parts[5] == "publishers" && parts[6] == "google" && parts[7] == "models" {
		model, operation, ok = splitVertexModelOperation(parts[8])
		return parts[2], parts[4], model, operation, ok
	}
	return "", "", "", "", false
}

func splitVertexModelOperation(value string) (string, string, bool) {
	separator := strings.LastIndex(value, ":")
	if separator <= 0 {
		return "", "", false
	}
	model, err := url.PathUnescape(value[:separator])
	if err != nil || model == "" {
		return "", "", false
	}
	operation := value[separator+1:]
	if operation != "generateContent" && operation != "streamGenerateContent" {
		return "", "", false
	}
	return model, operation, true
}

func vertexContentCharacters(content vertexContent) int {
	total := 0
	for _, part := range content.Parts {
		total += len([]rune(part.Text))
	}
	return total
}

func vertexGenerateResponse(responseID, model, text string, inputCharacters int) map[string]any {
	promptTokens := max(1, (inputCharacters+3)/4)
	outputTokens := max(1, (len([]rune(text))+3)/4)
	return map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": []any{map[string]string{"text": text}}},
			"finishReason": "STOP",
			"index":        0,
		}},
		"usageMetadata": map[string]int{
			"promptTokenCount": promptTokens, "candidatesTokenCount": outputTokens, "totalTokenCount": promptTokens + outputTokens,
		},
		"modelVersion": model,
		"responseId":   responseID,
	}
}

func mustJSON(value any) []byte {
	payload, _ := json.Marshal(value)
	return payload
}

func fcmAdminResponse(messages any) map[string]any {
	return map[string]any{"messages": messages, "hint": fmt.Sprintf("%s stores fake messages only", "FCP")}
}
