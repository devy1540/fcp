package server

import (
	"context"
	"errors"
	"hash/crc32"
	"strconv"
	"strings"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/hjyoon/fcp/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type secretManagerServer struct {
	secretmanagerpb.UnimplementedSecretManagerServiceServer
	store *state.Store
}

func newSecretManagerServer(store *state.Store) *secretManagerServer {
	return &secretManagerServer{store: store}
}

func (s *secretManagerServer) CreateSecret(_ context.Context, request *secretmanagerpb.CreateSecretRequest) (*secretmanagerpb.Secret, error) {
	if request.GetParent() == "" || request.GetSecretId() == "" {
		return nil, status.Error(codes.InvalidArgument, "parent and secret_id are required")
	}
	name := strings.TrimSuffix(request.GetParent(), "/") + "/secrets/" + request.GetSecretId()
	if _, err := s.store.Secret(name); err == nil {
		return nil, status.Error(codes.AlreadyExists, "secret already exists")
	}
	created, err := s.store.CreateSecret(name, request.GetSecret().GetLabels())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return secretProto(created), nil
}

func (s *secretManagerServer) GetSecret(_ context.Context, request *secretmanagerpb.GetSecretRequest) (*secretmanagerpb.Secret, error) {
	secret, err := s.store.Secret(request.GetName())
	if err != nil {
		return nil, secretManagerError(err)
	}
	return secretProto(secret), nil
}

func (s *secretManagerServer) ListSecrets(_ context.Context, request *secretmanagerpb.ListSecretsRequest) (*secretmanagerpb.ListSecretsResponse, error) {
	all := s.store.ListSecrets(request.GetParent())
	after := state.DecodePageToken(request.GetPageToken())
	pageSize := int(request.GetPageSize())
	if pageSize <= 0 {
		pageSize = 100
	}
	response := &secretmanagerpb.ListSecretsResponse{TotalSize: int32(len(all))}
	for _, secret := range all {
		if secret.Name <= after {
			continue
		}
		if len(response.Secrets) >= pageSize {
			response.NextPageToken = state.EncodePageToken(response.Secrets[len(response.Secrets)-1].GetName())
			break
		}
		response.Secrets = append(response.Secrets, secretProto(secret))
	}
	return response, nil
}

func (s *secretManagerServer) UpdateSecret(_ context.Context, request *secretmanagerpb.UpdateSecretRequest) (*secretmanagerpb.Secret, error) {
	// FCP only reads secrets. Returning the current metadata for an empty update
	// mask keeps official SDK setup code compatible without pretending to support
	// rotation or replication updates.
	if request.GetUpdateMask() != nil && len(request.GetUpdateMask().GetPaths()) > 0 {
		for _, path := range request.GetUpdateMask().GetPaths() {
			if path != "labels" {
				return nil, status.Errorf(codes.Unimplemented, "secret update field %q is not supported", path)
			}
		}
	}
	secret, err := s.store.Secret(request.GetSecret().GetName())
	if err != nil {
		return nil, secretManagerError(err)
	}
	return secretProto(secret), nil
}

func (s *secretManagerServer) DeleteSecret(_ context.Context, request *secretmanagerpb.DeleteSecretRequest) (*emptypb.Empty, error) {
	if err := s.store.DeleteSecret(request.GetName()); err != nil {
		return nil, secretManagerError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *secretManagerServer) AddSecretVersion(_ context.Context, request *secretmanagerpb.AddSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	if request.GetPayload() == nil {
		return nil, status.Error(codes.InvalidArgument, "payload is required")
	}
	if len(request.GetPayload().GetData()) > 64*1024 {
		return nil, status.Error(codes.InvalidArgument, "secret payload exceeds 64KiB")
	}
	if request.GetPayload().DataCrc32C != nil {
		actual := int64(crc32.Checksum(request.GetPayload().GetData(), crc32.MakeTable(crc32.Castagnoli)))
		if actual != request.GetPayload().GetDataCrc32C() {
			return nil, status.Error(codes.InvalidArgument, "secret payload CRC32C mismatch")
		}
	}
	version, err := s.store.AddSecretVersion(request.GetParent(), request.GetPayload().GetData())
	if err != nil {
		return nil, secretManagerError(err)
	}
	return secretVersionProto(request.GetParent(), version), nil
}

func (s *secretManagerServer) GetSecretVersion(_ context.Context, request *secretmanagerpb.GetSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	secretName, number, err := parseSecretVersionName(request.GetName())
	if err != nil {
		return nil, err
	}
	version, err := s.store.SecretVersion(secretName, number)
	if err != nil {
		return nil, secretManagerError(err)
	}
	return secretVersionProto(secretName, version), nil
}

func (s *secretManagerServer) AccessSecretVersion(_ context.Context, request *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	secretName, number, err := parseSecretVersionName(request.GetName())
	if err != nil {
		return nil, err
	}
	version, err := s.store.SecretVersion(secretName, number)
	if err != nil {
		return nil, secretManagerError(err)
	}
	if version.State != "ENABLED" {
		return nil, status.Error(codes.FailedPrecondition, "secret version is not enabled")
	}
	checksum := int64(crc32.Checksum(version.Payload, crc32.MakeTable(crc32.Castagnoli)))
	return &secretmanagerpb.AccessSecretVersionResponse{
		Name: secretVersionName(secretName, version.Number),
		Payload: &secretmanagerpb.SecretPayload{
			Data:       append([]byte(nil), version.Payload...),
			DataCrc32C: &checksum,
		},
	}, nil
}

func (s *secretManagerServer) ListSecretVersions(_ context.Context, request *secretmanagerpb.ListSecretVersionsRequest) (*secretmanagerpb.ListSecretVersionsResponse, error) {
	secret, err := s.store.Secret(request.GetParent())
	if err != nil {
		return nil, secretManagerError(err)
	}
	pageSize := int(request.GetPageSize())
	if pageSize <= 0 {
		pageSize = 100
	}
	after := state.DecodePageToken(request.GetPageToken())
	response := &secretmanagerpb.ListSecretVersionsResponse{TotalSize: int32(len(secret.Versions))}
	for i := len(secret.Versions) - 1; i >= 0; i-- {
		version := secret.Versions[i]
		name := secretVersionName(secret.Name, version.Number)
		if name <= after {
			continue
		}
		if len(response.Versions) >= pageSize {
			response.NextPageToken = state.EncodePageToken(response.Versions[len(response.Versions)-1].GetName())
			break
		}
		response.Versions = append(response.Versions, secretVersionProto(secret.Name, version))
	}
	return response, nil
}

func (s *secretManagerServer) DisableSecretVersion(_ context.Context, request *secretmanagerpb.DisableSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	return s.setSecretVersionState(request.GetName(), "DISABLED")
}

func (s *secretManagerServer) EnableSecretVersion(_ context.Context, request *secretmanagerpb.EnableSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	return s.setSecretVersionState(request.GetName(), "ENABLED")
}

func (s *secretManagerServer) DestroySecretVersion(_ context.Context, request *secretmanagerpb.DestroySecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	return s.setSecretVersionState(request.GetName(), "DESTROYED")
}

func (s *secretManagerServer) setSecretVersionState(name, versionState string) (*secretmanagerpb.SecretVersion, error) {
	secretName, number, err := parseSecretVersionName(name)
	if err != nil {
		return nil, err
	}
	if number == 0 {
		return nil, status.Error(codes.InvalidArgument, "latest alias is not accepted for state changes")
	}
	version, err := s.store.SetSecretVersionState(secretName, number, versionState)
	if err != nil {
		return nil, secretManagerError(err)
	}
	return secretVersionProto(secretName, version), nil
}

func secretProto(secret state.Secret) *secretmanagerpb.Secret {
	return &secretmanagerpb.Secret{
		Name:       secret.Name,
		Labels:     cloneSecretLabels(secret.Labels),
		CreateTime: timestamppb.New(secret.CreateTime),
		Replication: &secretmanagerpb.Replication{Replication: &secretmanagerpb.Replication_Automatic_{
			Automatic: &secretmanagerpb.Replication_Automatic{},
		}},
	}
}

func cloneSecretLabels(labels map[string]string) map[string]string {
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

func secretVersionProto(secretName string, version state.SecretVersion) *secretmanagerpb.SecretVersion {
	versionState := secretmanagerpb.SecretVersion_STATE_UNSPECIFIED
	switch version.State {
	case "ENABLED":
		versionState = secretmanagerpb.SecretVersion_ENABLED
	case "DISABLED":
		versionState = secretmanagerpb.SecretVersion_DISABLED
	case "DESTROYED":
		versionState = secretmanagerpb.SecretVersion_DESTROYED
	}
	return &secretmanagerpb.SecretVersion{
		Name:       secretVersionName(secretName, version.Number),
		CreateTime: timestamppb.New(version.CreateTime),
		State:      versionState,
	}
}

func parseSecretVersionName(name string) (string, int64, error) {
	marker := "/versions/"
	index := strings.LastIndex(name, marker)
	if index < 0 {
		return "", 0, status.Error(codes.InvalidArgument, "invalid secret version name")
	}
	secretName := name[:index]
	versionID := name[index+len(marker):]
	if versionID == "latest" {
		return secretName, 0, nil
	}
	number, err := strconv.ParseInt(versionID, 10, 64)
	if err != nil || number <= 0 {
		return "", 0, status.Error(codes.InvalidArgument, "invalid secret version number")
	}
	return secretName, number, nil
}

func secretVersionName(secretName string, number int64) string {
	return secretName + "/versions/" + strconv.FormatInt(number, 10)
}

func secretManagerError(err error) error {
	if errors.Is(err, state.ErrSecretNotFound) || errors.Is(err, state.ErrSecretVersionNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}
