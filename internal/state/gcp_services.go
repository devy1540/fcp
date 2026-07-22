package state

import (
	"errors"
	"sort"
	"strings"
	"time"
)

var (
	ErrFirestoreDocumentNotFound = errors.New("Firestore document not found")
	ErrSecretNotFound            = errors.New("Secret Manager secret not found")
	ErrSecretVersionNotFound     = errors.New("Secret Manager secret version not found")
)

// FirestoreDocument stores the wire representation of a Firestore Document.
// Keeping protobuf bytes in the state layer avoids coupling persistence to the
// rapidly changing generated protobuf Go structs.
type FirestoreDocument struct {
	Name       string    `json:"name"`
	Proto      []byte    `json:"proto"`
	CreateTime time.Time `json:"createTime"`
	UpdateTime time.Time `json:"updateTime"`
}

func cloneFirestoreDocument(document *FirestoreDocument) FirestoreDocument {
	cloned := *document
	cloned.Proto = append([]byte(nil), document.Proto...)
	return cloned
}

func (s *Store) FirestoreDocument(name string) (FirestoreDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.data.FirestoreDocuments[name]
	if !ok {
		return FirestoreDocument{}, ErrFirestoreDocumentNotFound
	}
	return cloneFirestoreDocument(document), nil
}

func (s *Store) ListFirestoreDocuments(prefix string) []FirestoreDocument {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]FirestoreDocument, 0)
	for name, document := range s.data.FirestoreDocuments {
		if strings.HasPrefix(name, prefix) {
			result = append(result, cloneFirestoreDocument(document))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// MutateFirestore applies a set of document changes under one store lock and
// persists one snapshot. The callback receives an isolated map, so returning
// an error leaves the stored state untouched.
func (s *Store) MutateFirestore(mutate func(map[string]*FirestoreDocument, time.Time) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	working := make(map[string]*FirestoreDocument, len(s.data.FirestoreDocuments))
	for name, document := range s.data.FirestoreDocuments {
		cloned := cloneFirestoreDocument(document)
		working[name] = &cloned
	}
	if err := mutate(working, s.now().UTC()); err != nil {
		return err
	}
	previous := s.data.FirestoreDocuments
	s.data.FirestoreDocuments = working
	if err := s.saveLocked(); err != nil {
		s.data.FirestoreDocuments = previous
		return err
	}
	return nil
}

type SecretVersion struct {
	Number     int64     `json:"number"`
	Payload    []byte    `json:"payload,omitempty"`
	State      string    `json:"state"`
	CreateTime time.Time `json:"createTime"`
}

type Secret struct {
	Name       string            `json:"name"`
	Labels     map[string]string `json:"labels,omitempty"`
	CreateTime time.Time         `json:"createTime"`
	Versions   []SecretVersion   `json:"versions,omitempty"`
}

func cloneSecret(secret *Secret) Secret {
	cloned := *secret
	cloned.Labels = cloneStringMap(secret.Labels)
	cloned.Versions = make([]SecretVersion, len(secret.Versions))
	copy(cloned.Versions, secret.Versions)
	for i := range cloned.Versions {
		cloned.Versions[i].Payload = append([]byte(nil), secret.Versions[i].Payload...)
	}
	return cloned
}

func (s *Store) CreateSecret(name string, labels map[string]string) (Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.data.Secrets[name]; ok {
		return cloneSecret(existing), nil
	}
	secret := &Secret{Name: name, Labels: cloneStringMap(labels), CreateTime: s.now().UTC()}
	s.data.Secrets[name] = secret
	if err := s.saveLocked(); err != nil {
		delete(s.data.Secrets, name)
		return Secret{}, err
	}
	return cloneSecret(secret), nil
}

func (s *Store) Secret(name string) (Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, ok := s.data.Secrets[name]
	if !ok {
		return Secret{}, ErrSecretNotFound
	}
	return cloneSecret(secret), nil
}

func (s *Store) ListSecrets(parent string) []Secret {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := ""
	if parent != "" {
		prefix = strings.TrimSuffix(parent, "/") + "/secrets/"
	}
	result := make([]Secret, 0)
	for name, secret := range s.data.Secrets {
		if strings.HasPrefix(name, prefix) {
			result = append(result, cloneSecret(secret))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) DeleteSecret(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, ok := s.data.Secrets[name]
	if !ok {
		return ErrSecretNotFound
	}
	delete(s.data.Secrets, name)
	if err := s.saveLocked(); err != nil {
		s.data.Secrets[name] = secret
		return err
	}
	return nil
}

func (s *Store) AddSecretVersion(name string, payload []byte) (SecretVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, ok := s.data.Secrets[name]
	if !ok {
		return SecretVersion{}, ErrSecretNotFound
	}
	version := SecretVersion{
		Number:     int64(len(secret.Versions) + 1),
		Payload:    append([]byte(nil), payload...),
		State:      "ENABLED",
		CreateTime: s.now().UTC(),
	}
	secret.Versions = append(secret.Versions, version)
	if err := s.saveLocked(); err != nil {
		secret.Versions = secret.Versions[:len(secret.Versions)-1]
		return SecretVersion{}, err
	}
	return version, nil
}

func (s *Store) SecretVersion(name string, number int64) (SecretVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, ok := s.data.Secrets[name]
	if !ok {
		return SecretVersion{}, ErrSecretNotFound
	}
	if number == 0 {
		for i := len(secret.Versions) - 1; i >= 0; i-- {
			if secret.Versions[i].State == "ENABLED" {
				version := secret.Versions[i]
				version.Payload = append([]byte(nil), version.Payload...)
				return version, nil
			}
		}
	}
	for _, version := range secret.Versions {
		if version.Number == number {
			version.Payload = append([]byte(nil), version.Payload...)
			return version, nil
		}
	}
	return SecretVersion{}, ErrSecretVersionNotFound
}

func (s *Store) SetSecretVersionState(name string, number int64, versionState string) (SecretVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, ok := s.data.Secrets[name]
	if !ok {
		return SecretVersion{}, ErrSecretNotFound
	}
	for i := range secret.Versions {
		if secret.Versions[i].Number != number {
			continue
		}
		previousState := secret.Versions[i].State
		previousPayload := append([]byte(nil), secret.Versions[i].Payload...)
		secret.Versions[i].State = versionState
		if versionState == "DESTROYED" {
			secret.Versions[i].Payload = nil
		}
		if err := s.saveLocked(); err != nil {
			secret.Versions[i].State = previousState
			secret.Versions[i].Payload = previousPayload
			return SecretVersion{}, err
		}
		return secret.Versions[i], nil
	}
	return SecretVersion{}, ErrSecretVersionNotFound
}
