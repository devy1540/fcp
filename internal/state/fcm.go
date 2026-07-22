package state

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

type FCMMessage struct {
	Name         string          `json:"name"`
	Project      string          `json:"project"`
	ValidateOnly bool            `json:"validateOnly,omitempty"`
	Message      json.RawMessage `json:"message"`
	CreatedAt    time.Time       `json:"createdAt"`
}

func cloneFCMMessage(message FCMMessage) FCMMessage {
	message.Message = append(json.RawMessage(nil), message.Message...)
	return message
}

func (s *Store) RecordFCMMessage(project string, message json.RawMessage, validateOnly bool) (FCMMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recorded := FCMMessage{
		Name:         "projects/" + project + "/messages/" + newID(),
		Project:      project,
		ValidateOnly: validateOnly,
		Message:      append(json.RawMessage(nil), message...),
		CreatedAt:    s.now().UTC(),
	}
	s.data.FCMMessages = append(s.data.FCMMessages, recorded)
	if err := s.saveLocked(); err != nil {
		s.data.FCMMessages = s.data.FCMMessages[:len(s.data.FCMMessages)-1]
		return FCMMessage{}, err
	}
	return cloneFCMMessage(recorded), nil
}

func (s *Store) ListFCMMessages(project string) []FCMMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]FCMMessage, 0, len(s.data.FCMMessages))
	for _, message := range s.data.FCMMessages {
		if project == "" || strings.EqualFold(message.Project, project) {
			result = append(result, cloneFCMMessage(message))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (s *Store) ClearFCMMessages() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.data.FCMMessages
	s.data.FCMMessages = []FCMMessage{}
	if err := s.saveLocked(); err != nil {
		s.data.FCMMessages = previous
		return err
	}
	return nil
}
