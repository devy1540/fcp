package state

import (
	"errors"
	"sort"
	"strings"
	"time"
)

var (
	ErrKMSKeyRingNotFound   = errors.New("Cloud KMS key ring not found")
	ErrKMSCryptoKeyNotFound = errors.New("Cloud KMS crypto key not found")
	ErrKMSKeyVersionMissing = errors.New("Cloud KMS key version not found")
)

type KMSKeyRing struct {
	Name       string    `json:"name"`
	CreateTime time.Time `json:"createTime"`
}

type KMSKeyVersion struct {
	Number      int64     `json:"number"`
	Algorithm   string    `json:"algorithm"`
	State       string    `json:"state"`
	KeyMaterial []byte    `json:"keyMaterial"`
	CreateTime  time.Time `json:"createTime"`
}

type KMSCryptoKey struct {
	Name           string          `json:"name"`
	Purpose        string          `json:"purpose"`
	Algorithm      string          `json:"algorithm"`
	PrimaryVersion int64           `json:"primaryVersion"`
	CreateTime     time.Time       `json:"createTime"`
	Versions       []KMSKeyVersion `json:"versions"`
}

func cloneKMSCryptoKey(key *KMSCryptoKey) KMSCryptoKey {
	cloned := *key
	cloned.Versions = make([]KMSKeyVersion, len(key.Versions))
	copy(cloned.Versions, key.Versions)
	for i := range cloned.Versions {
		cloned.Versions[i].KeyMaterial = append([]byte(nil), key.Versions[i].KeyMaterial...)
	}
	return cloned
}

func (s *Store) CreateKMSKeyRing(name string) (KMSKeyRing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.data.KMSKeyRings[name]; ok {
		return *existing, nil
	}
	keyRing := &KMSKeyRing{Name: name, CreateTime: s.now().UTC()}
	s.data.KMSKeyRings[name] = keyRing
	if err := s.saveLocked(); err != nil {
		delete(s.data.KMSKeyRings, name)
		return KMSKeyRing{}, err
	}
	return *keyRing, nil
}

func (s *Store) KMSKeyRing(name string) (KMSKeyRing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keyRing, ok := s.data.KMSKeyRings[name]
	if !ok {
		return KMSKeyRing{}, ErrKMSKeyRingNotFound
	}
	return *keyRing, nil
}

func (s *Store) ListKMSKeyRings(parent string) []KMSKeyRing {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := ""
	if parent != "" {
		prefix = strings.TrimSuffix(parent, "/") + "/keyRings/"
	}
	result := make([]KMSKeyRing, 0)
	for name, keyRing := range s.data.KMSKeyRings {
		if strings.HasPrefix(name, prefix) {
			result = append(result, *keyRing)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) CreateKMSCryptoKey(key KMSCryptoKey) (KMSCryptoKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keyRingName := key.Name[:strings.LastIndex(key.Name, "/cryptoKeys/")]
	if _, ok := s.data.KMSKeyRings[keyRingName]; !ok {
		return KMSCryptoKey{}, ErrKMSKeyRingNotFound
	}
	if existing, ok := s.data.KMSCryptoKeys[key.Name]; ok {
		return cloneKMSCryptoKey(existing), nil
	}
	cloned := cloneKMSCryptoKey(&key)
	s.data.KMSCryptoKeys[key.Name] = &cloned
	if err := s.saveLocked(); err != nil {
		delete(s.data.KMSCryptoKeys, key.Name)
		return KMSCryptoKey{}, err
	}
	return cloneKMSCryptoKey(&cloned), nil
}

func (s *Store) KMSCryptoKey(name string) (KMSCryptoKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.data.KMSCryptoKeys[name]
	if !ok {
		return KMSCryptoKey{}, ErrKMSCryptoKeyNotFound
	}
	return cloneKMSCryptoKey(key), nil
}

func (s *Store) ListKMSCryptoKeys(parent string) []KMSCryptoKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := ""
	if parent != "" {
		prefix = strings.TrimSuffix(parent, "/") + "/cryptoKeys/"
	}
	result := make([]KMSCryptoKey, 0)
	for name, key := range s.data.KMSCryptoKeys {
		if strings.HasPrefix(name, prefix) {
			result = append(result, cloneKMSCryptoKey(key))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) AddKMSKeyVersion(name, algorithm string, material []byte) (KMSKeyVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.data.KMSCryptoKeys[name]
	if !ok {
		return KMSKeyVersion{}, ErrKMSCryptoKeyNotFound
	}
	version := KMSKeyVersion{
		Number:      int64(len(key.Versions) + 1),
		Algorithm:   algorithm,
		State:       "ENABLED",
		KeyMaterial: append([]byte(nil), material...),
		CreateTime:  s.now().UTC(),
	}
	key.Versions = append(key.Versions, version)
	if key.PrimaryVersion == 0 {
		key.PrimaryVersion = version.Number
	}
	if err := s.saveLocked(); err != nil {
		key.Versions = key.Versions[:len(key.Versions)-1]
		return KMSKeyVersion{}, err
	}
	return version, nil
}

func (s *Store) KMSKeyVersion(keyName string, number int64) (KMSKeyVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.data.KMSCryptoKeys[keyName]
	if !ok {
		return KMSKeyVersion{}, ErrKMSCryptoKeyNotFound
	}
	if number == 0 {
		number = key.PrimaryVersion
	}
	for _, version := range key.Versions {
		if version.Number == number {
			version.KeyMaterial = append([]byte(nil), version.KeyMaterial...)
			return version, nil
		}
	}
	return KMSKeyVersion{}, ErrKMSKeyVersionMissing
}
