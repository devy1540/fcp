package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devy1540/fcp/internal/state"
)

func TestSnapshotAdminAPI(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("before"); err != nil {
		t.Fatal(err)
	}
	handler := New(store)

	save := snapshotRequestJSON(t, handler, map[string]string{"operation": "save", "name": "baseline"})
	if save.Code != http.StatusCreated || !strings.Contains(save.Body.String(), `"name":"baseline"`) {
		t.Fatalf("save status=%d body=%s", save.Code, save.Body.String())
	}
	if err := store.CreateBucket("after"); err != nil {
		t.Fatal(err)
	}
	load := snapshotRequestJSON(t, handler, map[string]string{"operation": "load", "name": "baseline"})
	if load.Code != http.StatusOK || store.HasBucket("after") || !store.HasBucket("before") {
		t.Fatalf("load status=%d body=%s buckets=%+v", load.Code, load.Body.String(), store.ListBuckets())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/_fcp/snapshots", nil)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), `"containsSensitiveData":true`) || strings.Contains(listResponse.Body.String(), `"Buckets"`) {
		t.Fatalf("list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}

	deleteResponse := snapshotRequestJSON(t, handler, map[string]string{"operation": "delete", "name": "baseline"})
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}
	missing := snapshotRequestJSON(t, handler, map[string]string{"operation": "load", "name": "baseline"})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestSnapshotAdminAPIRejectsInvalidRequests(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store)
	request := httptest.NewRequest(http.MethodPost, "/_fcp/snapshots", strings.NewReader(`{"operation":"save","name":"../bad"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func snapshotRequestJSON(t *testing.T, handler http.Handler, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/_fcp/snapshots", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
