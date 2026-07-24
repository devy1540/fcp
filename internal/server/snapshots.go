package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/devy1540/fcp/internal/state"
)

type snapshotRequest struct {
	Operation string `json:"operation"`
	Name      string `json:"name"`
}

func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		snapshots, err := s.store.ListSnapshots()
		if err != nil {
			writeSnapshotError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"snapshots": snapshots,
			"warning":   "Snapshots can contain local Secret payloads and private key material; keep them local.",
		})
	case http.MethodPost:
		s.handleSnapshotMutation(w, r)
	default:
		w.Header().Set("Allow", strings.Join([]string{http.MethodGet, http.MethodPost}, ", "))
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSnapshotMutation(w http.ResponseWriter, r *http.Request) {
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "application/json request body is required"})
		return
	}
	var request snapshotRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid snapshot request"})
		return
	}
	request.Operation = strings.ToLower(strings.TrimSpace(request.Operation))
	request.Name = strings.TrimSpace(request.Name)

	switch request.Operation {
	case "save":
		snapshot, err := s.store.SaveSnapshot(request.Name)
		if err != nil {
			writeSnapshotError(w, snapshotErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"snapshot": snapshot})
	case "load":
		snapshot, err := s.store.LoadSnapshot(request.Name)
		if err != nil {
			writeSnapshotError(w, snapshotErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"snapshot": snapshot})
	case "delete":
		if err := s.store.DeleteSnapshot(request.Name); err != nil {
			writeSnapshotError(w, snapshotErrorStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": request.Name})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "operation must be save, load, or delete"})
	}
}

func snapshotErrorStatus(err error) int {
	switch {
	case errors.Is(err, state.ErrSnapshotInvalidName):
		return http.StatusBadRequest
	case errors.Is(err, state.ErrSnapshotNotFound):
		return http.StatusNotFound
	case errors.Is(err, state.ErrSnapshotExists):
		return http.StatusConflict
	case errors.Is(err, state.ErrSnapshotCorrupt):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

func writeSnapshotError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
