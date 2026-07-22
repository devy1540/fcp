package server

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/hjyoon/fcp/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const fcpKMSCiphertextPrefix = "FCPKMS1"

type kmsServer struct {
	kmspb.UnimplementedKeyManagementServiceServer
	store *state.Store
}

func newKMSServer(store *state.Store) *kmsServer {
	return &kmsServer{store: store}
}

func (s *kmsServer) CreateKeyRing(_ context.Context, request *kmspb.CreateKeyRingRequest) (*kmspb.KeyRing, error) {
	if request.GetParent() == "" || request.GetKeyRingId() == "" {
		return nil, status.Error(codes.InvalidArgument, "parent and key_ring_id are required")
	}
	name := strings.TrimSuffix(request.GetParent(), "/") + "/keyRings/" + request.GetKeyRingId()
	if _, err := s.store.KMSKeyRing(name); err == nil {
		return nil, status.Error(codes.AlreadyExists, "key ring already exists")
	}
	created, err := s.store.CreateKMSKeyRing(name)
	if err != nil {
		return nil, kmsError(err)
	}
	return kmsKeyRingProto(created), nil
}

func (s *kmsServer) GetKeyRing(_ context.Context, request *kmspb.GetKeyRingRequest) (*kmspb.KeyRing, error) {
	keyRing, err := s.store.KMSKeyRing(request.GetName())
	if err != nil {
		return nil, kmsError(err)
	}
	return kmsKeyRingProto(keyRing), nil
}

func (s *kmsServer) ListKeyRings(_ context.Context, request *kmspb.ListKeyRingsRequest) (*kmspb.ListKeyRingsResponse, error) {
	all := s.store.ListKMSKeyRings(request.GetParent())
	response := &kmspb.ListKeyRingsResponse{TotalSize: int32(len(all))}
	for _, keyRing := range all {
		response.KeyRings = append(response.KeyRings, kmsKeyRingProto(keyRing))
	}
	return response, nil
}

func (s *kmsServer) CreateCryptoKey(_ context.Context, request *kmspb.CreateCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	if request.GetParent() == "" || request.GetCryptoKeyId() == "" || request.GetCryptoKey() == nil {
		return nil, status.Error(codes.InvalidArgument, "parent, crypto_key_id and crypto_key are required")
	}
	name := strings.TrimSuffix(request.GetParent(), "/") + "/cryptoKeys/" + request.GetCryptoKeyId()
	if _, err := s.store.KMSCryptoKey(name); err == nil {
		return nil, status.Error(codes.AlreadyExists, "crypto key already exists")
	}
	algorithm := request.GetCryptoKey().GetVersionTemplate().GetAlgorithm()
	if algorithm == kmspb.CryptoKeyVersion_CRYPTO_KEY_VERSION_ALGORITHM_UNSPECIFIED {
		if request.GetCryptoKey().GetPurpose() == kmspb.CryptoKey_ENCRYPT_DECRYPT {
			algorithm = kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION
		} else {
			return nil, status.Error(codes.InvalidArgument, "crypto key version algorithm is required")
		}
	}
	createdAt := timeNowUTC()
	key := state.KMSCryptoKey{
		Name:       name,
		Purpose:    request.GetCryptoKey().GetPurpose().String(),
		Algorithm:  algorithm.String(),
		CreateTime: createdAt,
	}
	if !request.GetSkipInitialVersionCreation() {
		material, err := generateKMSKeyMaterial(algorithm)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		key.PrimaryVersion = 1
		key.Versions = []state.KMSKeyVersion{{Number: 1, Algorithm: algorithm.String(), State: "ENABLED", KeyMaterial: material, CreateTime: createdAt}}
	}
	created, err := s.store.CreateKMSCryptoKey(key)
	if err != nil {
		return nil, kmsError(err)
	}
	return kmsCryptoKeyProto(created), nil
}

func (s *kmsServer) GetCryptoKey(_ context.Context, request *kmspb.GetCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	key, err := s.store.KMSCryptoKey(request.GetName())
	if err != nil {
		return nil, kmsError(err)
	}
	return kmsCryptoKeyProto(key), nil
}

func (s *kmsServer) ListCryptoKeys(_ context.Context, request *kmspb.ListCryptoKeysRequest) (*kmspb.ListCryptoKeysResponse, error) {
	all := s.store.ListKMSCryptoKeys(request.GetParent())
	response := &kmspb.ListCryptoKeysResponse{TotalSize: int32(len(all))}
	for _, key := range all {
		response.CryptoKeys = append(response.CryptoKeys, kmsCryptoKeyProto(key))
	}
	return response, nil
}

func (s *kmsServer) CreateCryptoKeyVersion(_ context.Context, request *kmspb.CreateCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	key, err := s.store.KMSCryptoKey(request.GetParent())
	if err != nil {
		return nil, kmsError(err)
	}
	algorithm := kmsAlgorithm(key.Algorithm)
	if request.GetCryptoKeyVersion().GetAlgorithm() != kmspb.CryptoKeyVersion_CRYPTO_KEY_VERSION_ALGORITHM_UNSPECIFIED {
		algorithm = request.GetCryptoKeyVersion().GetAlgorithm()
	}
	material, err := generateKMSKeyMaterial(algorithm)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	version, err := s.store.AddKMSKeyVersion(key.Name, algorithm.String(), material)
	if err != nil {
		return nil, kmsError(err)
	}
	return kmsKeyVersionProto(key.Name, version), nil
}

func (s *kmsServer) GetCryptoKeyVersion(_ context.Context, request *kmspb.GetCryptoKeyVersionRequest) (*kmspb.CryptoKeyVersion, error) {
	keyName, number, err := parseKMSVersionName(request.GetName())
	if err != nil {
		return nil, err
	}
	version, err := s.store.KMSKeyVersion(keyName, number)
	if err != nil {
		return nil, kmsError(err)
	}
	return kmsKeyVersionProto(keyName, version), nil
}

func (s *kmsServer) ListCryptoKeyVersions(_ context.Context, request *kmspb.ListCryptoKeyVersionsRequest) (*kmspb.ListCryptoKeyVersionsResponse, error) {
	key, err := s.store.KMSCryptoKey(request.GetParent())
	if err != nil {
		return nil, kmsError(err)
	}
	response := &kmspb.ListCryptoKeyVersionsResponse{TotalSize: int32(len(key.Versions))}
	for _, version := range key.Versions {
		response.CryptoKeyVersions = append(response.CryptoKeyVersions, kmsKeyVersionProto(key.Name, version))
	}
	return response, nil
}

func (s *kmsServer) Encrypt(_ context.Context, request *kmspb.EncryptRequest) (*kmspb.EncryptResponse, error) {
	key, err := s.store.KMSCryptoKey(request.GetName())
	if err != nil {
		return nil, kmsError(err)
	}
	version, err := s.store.KMSKeyVersion(key.Name, 0)
	if err != nil {
		return nil, kmsError(err)
	}
	if version.Algorithm != kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION.String() {
		return nil, status.Error(codes.FailedPrecondition, "crypto key is not symmetric")
	}
	ciphertext, err := encryptKMS(version.KeyMaterial, request.GetPlaintext(), request.GetAdditionalAuthenticatedData())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	checksum := crc32c(ciphertext)
	return &kmspb.EncryptResponse{
		Name:                    kmsVersionName(key.Name, version.Number),
		Ciphertext:              ciphertext,
		CiphertextCrc32C:        wrapperspb.Int64(checksum),
		VerifiedPlaintextCrc32C: request.GetPlaintextCrc32C() != nil,
		VerifiedAdditionalAuthenticatedDataCrc32C: request.GetAdditionalAuthenticatedDataCrc32C() != nil,
		ProtectionLevel: kmspb.ProtectionLevel_SOFTWARE,
	}, nil
}

func (s *kmsServer) Decrypt(_ context.Context, request *kmspb.DecryptRequest) (*kmspb.DecryptResponse, error) {
	keyName := request.GetName()
	versionNumber := int64(0)
	if strings.Contains(keyName, "/cryptoKeyVersions/") {
		var err error
		keyName, versionNumber, err = parseKMSVersionName(keyName)
		if err != nil {
			return nil, err
		}
	}
	version, err := s.store.KMSKeyVersion(keyName, versionNumber)
	if err != nil {
		return nil, kmsError(err)
	}
	plaintext, err := decryptKMS(version.KeyMaterial, request.GetCiphertext(), request.GetAdditionalAuthenticatedData())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "ciphertext authentication failed")
	}
	return &kmspb.DecryptResponse{
		Plaintext:       plaintext,
		PlaintextCrc32C: wrapperspb.Int64(crc32c(plaintext)),
		UsedPrimary:     versionNumber == 0,
		ProtectionLevel: kmspb.ProtectionLevel_SOFTWARE,
	}, nil
}

func (s *kmsServer) GetPublicKey(_ context.Context, request *kmspb.GetPublicKeyRequest) (*kmspb.PublicKey, error) {
	keyName, number, err := parseKMSVersionName(request.GetName())
	if err != nil {
		return nil, err
	}
	version, err := s.store.KMSKeyVersion(keyName, number)
	if err != nil {
		return nil, kmsError(err)
	}
	privateKey, err := parseKMSRSAPrivateKey(version.KeyMaterial)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "key version is not an RSA signing key")
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	pemValue := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	return &kmspb.PublicKey{
		Pem:             pemValue,
		Algorithm:       kmsAlgorithm(version.Algorithm),
		PemCrc32C:       wrapperspb.Int64(crc32c([]byte(pemValue))),
		Name:            request.GetName(),
		ProtectionLevel: kmspb.ProtectionLevel_SOFTWARE,
	}, nil
}

func (s *kmsServer) AsymmetricSign(_ context.Context, request *kmspb.AsymmetricSignRequest) (*kmspb.AsymmetricSignResponse, error) {
	keyName, number, err := parseKMSVersionName(request.GetName())
	if err != nil {
		return nil, err
	}
	version, err := s.store.KMSKeyVersion(keyName, number)
	if err != nil {
		return nil, kmsError(err)
	}
	privateKey, err := parseKMSRSAPrivateKey(version.KeyMaterial)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "key version is not an RSA signing key")
	}
	digest := request.GetDigest().GetSha256()
	if len(digest) != crypto.SHA256.Size() {
		return nil, status.Error(codes.InvalidArgument, "SHA-256 digest is required")
	}
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &kmspb.AsymmetricSignResponse{
		Signature:            signature,
		SignatureCrc32C:      wrapperspb.Int64(crc32c(signature)),
		VerifiedDigestCrc32C: request.GetDigestCrc32C() != nil,
		Name:                 request.GetName(),
		ProtectionLevel:      kmspb.ProtectionLevel_SOFTWARE,
	}, nil
}

func generateKMSKeyMaterial(algorithm kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm) ([]byte, error) {
	switch algorithm {
	case kmspb.CryptoKeyVersion_GOOGLE_SYMMETRIC_ENCRYPTION:
		material := make([]byte, 32)
		_, err := rand.Read(material)
		return material, err
	case kmspb.CryptoKeyVersion_RSA_SIGN_PKCS1_2048_SHA256:
		return generateRSAPrivateKey(2048)
	case kmspb.CryptoKeyVersion_RSA_SIGN_PKCS1_3072_SHA256:
		return generateRSAPrivateKey(3072)
	case kmspb.CryptoKeyVersion_RSA_SIGN_PKCS1_4096_SHA256:
		return generateRSAPrivateKey(4096)
	default:
		return nil, fmt.Errorf("unsupported KMS algorithm: %s", algorithm)
	}
}

func generateRSAPrivateKey(bits int) ([]byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return nil, err
	}
	return x509.MarshalPKCS8PrivateKey(privateKey)
}

func parseKMSRSAPrivateKey(material []byte) (*rsa.PrivateKey, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(material)
	if err != nil {
		return nil, err
	}
	privateKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA private key")
	}
	return privateKey, nil
}

func encryptKMS(material, plaintext, additionalData []byte) ([]byte, error) {
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plaintext, additionalData)
	return append(append([]byte(fcpKMSCiphertextPrefix), nonce...), sealed...), nil
}

func decryptKMS(material, ciphertext, additionalData []byte) ([]byte, error) {
	if !strings.HasPrefix(string(ciphertext), fcpKMSCiphertextPrefix) {
		return nil, errors.New("not an FCP KMS ciphertext")
	}
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	payload := ciphertext[len(fcpKMSCiphertextPrefix):]
	if len(payload) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, payload[:gcm.NonceSize()], payload[gcm.NonceSize():], additionalData)
}

func kmsKeyRingProto(keyRing state.KMSKeyRing) *kmspb.KeyRing {
	return &kmspb.KeyRing{Name: keyRing.Name, CreateTime: timestamppb.New(keyRing.CreateTime)}
}

func kmsCryptoKeyProto(key state.KMSCryptoKey) *kmspb.CryptoKey {
	result := &kmspb.CryptoKey{
		Name:       key.Name,
		Purpose:    kmsPurpose(key.Purpose),
		CreateTime: timestamppb.New(key.CreateTime),
		VersionTemplate: &kmspb.CryptoKeyVersionTemplate{
			Algorithm:       kmsAlgorithm(key.Algorithm),
			ProtectionLevel: kmspb.ProtectionLevel_SOFTWARE,
		},
	}
	if key.PrimaryVersion > 0 {
		for _, version := range key.Versions {
			if version.Number == key.PrimaryVersion {
				result.Primary = kmsKeyVersionProto(key.Name, version)
				break
			}
		}
	}
	return result
}

func kmsKeyVersionProto(keyName string, version state.KMSKeyVersion) *kmspb.CryptoKeyVersion {
	return &kmspb.CryptoKeyVersion{
		Name:            kmsVersionName(keyName, version.Number),
		State:           kmspb.CryptoKeyVersion_ENABLED,
		Algorithm:       kmsAlgorithm(version.Algorithm),
		ProtectionLevel: kmspb.ProtectionLevel_SOFTWARE,
		CreateTime:      timestamppb.New(version.CreateTime),
	}
}

func kmsVersionName(keyName string, number int64) string {
	return keyName + "/cryptoKeyVersions/" + strconv.FormatInt(number, 10)
}

func parseKMSVersionName(name string) (string, int64, error) {
	const marker = "/cryptoKeyVersions/"
	index := strings.LastIndex(name, marker)
	if index < 0 {
		return "", 0, status.Error(codes.InvalidArgument, "invalid crypto key version name")
	}
	number, err := strconv.ParseInt(name[index+len(marker):], 10, 64)
	if err != nil || number <= 0 {
		return "", 0, status.Error(codes.InvalidArgument, "invalid crypto key version number")
	}
	return name[:index], number, nil
}

func kmsAlgorithm(value string) kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm {
	return kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm(kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm_value[value])
}

func kmsPurpose(value string) kmspb.CryptoKey_CryptoKeyPurpose {
	return kmspb.CryptoKey_CryptoKeyPurpose(kmspb.CryptoKey_CryptoKeyPurpose_value[value])
}

func crc32c(data []byte) int64 {
	return int64(crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli)))
}

func kmsError(err error) error {
	if errors.Is(err, state.ErrKMSKeyRingNotFound) || errors.Is(err, state.ErrKMSCryptoKeyNotFound) || errors.Is(err, state.ErrKMSKeyVersionMissing) {
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}

// timeNowUTC is kept in one place so generated key metadata is internally
// consistent before it is persisted by the state store.
func timeNowUTC() time.Time {
	return time.Now().UTC()
}
