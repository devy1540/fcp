package state

import (
	"errors"
	"sort"
	"time"
)

var ErrIAMServiceAccountNotFound = errors.New("IAM service account not found")

type IAMServiceAccount struct {
	Name       string    `json:"name"`
	KeyID      string    `json:"keyId"`
	PrivateKey []byte    `json:"privateKey"`
	CreateTime time.Time `json:"createTime"`
}

func (s *Store) ListIAMServiceAccounts() []IAMServiceAccount {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]IAMServiceAccount, 0, len(s.data.IAMServiceAccounts))
	for _, account := range s.data.IAMServiceAccounts {
		cloned := *account
		cloned.PrivateKey = append([]byte(nil), account.PrivateKey...)
		result = append(result, cloned)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *Store) ExistingIAMServiceAccount(name string) (IAMServiceAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.data.IAMServiceAccounts[name]
	if !ok {
		return IAMServiceAccount{}, ErrIAMServiceAccountNotFound
	}
	cloned := *account
	cloned.PrivateKey = append([]byte(nil), account.PrivateKey...)
	return cloned, nil
}

func (s *Store) IAMServiceAccount(name string, generate func() ([]byte, error)) (IAMServiceAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if account, ok := s.data.IAMServiceAccounts[name]; ok {
		cloned := *account
		cloned.PrivateKey = append([]byte(nil), account.PrivateKey...)
		return cloned, nil
	}
	privateKey, err := generate()
	if err != nil {
		return IAMServiceAccount{}, err
	}
	account := &IAMServiceAccount{
		Name:       name,
		KeyID:      newID(),
		PrivateKey: append([]byte(nil), privateKey...),
		CreateTime: s.now().UTC(),
	}
	s.data.IAMServiceAccounts[name] = account
	if err := s.saveLocked(); err != nil {
		delete(s.data.IAMServiceAccounts, name)
		return IAMServiceAccount{}, err
	}
	cloned := *account
	cloned.PrivateKey = append([]byte(nil), account.PrivateKey...)
	return cloned, nil
}
