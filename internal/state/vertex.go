package state

import (
	"sort"
	"strings"
	"time"
)

// VertexGeneration stores only call metadata. Prompt and generated content are
// intentionally excluded because local E2E traffic may contain personal data.
type VertexGeneration struct {
	Name            string    `json:"name"`
	Project         string    `json:"project"`
	Location        string    `json:"location"`
	Model           string    `json:"model"`
	Operation       string    `json:"operation"`
	InputCharacters int       `json:"inputCharacters"`
	ToolCount       int       `json:"toolCount"`
	CreatedAt       time.Time `json:"createdAt"`
}

func (s *Store) RecordVertexGeneration(project, location, model, operation string, inputCharacters, toolCount int) (VertexGeneration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recorded := VertexGeneration{
		Name:            "projects/" + project + "/locations/" + location + "/generations/" + newID(),
		Project:         project,
		Location:        location,
		Model:           model,
		Operation:       operation,
		InputCharacters: inputCharacters,
		ToolCount:       toolCount,
		CreatedAt:       s.now().UTC(),
	}
	s.data.VertexGenerations = append(s.data.VertexGenerations, recorded)
	if err := s.saveLocked(); err != nil {
		s.data.VertexGenerations = s.data.VertexGenerations[:len(s.data.VertexGenerations)-1]
		return VertexGeneration{}, err
	}
	return recorded, nil
}

func (s *Store) ListVertexGenerations(project string) []VertexGeneration {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]VertexGeneration, 0, len(s.data.VertexGenerations))
	for _, generation := range s.data.VertexGenerations {
		if project == "" || strings.EqualFold(generation.Project, project) {
			result = append(result, generation)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (s *Store) ClearVertexGenerations() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.data.VertexGenerations
	s.data.VertexGenerations = []VertexGeneration{}
	if err := s.saveLocked(); err != nil {
		s.data.VertexGenerations = previous
		return err
	}
	return nil
}
