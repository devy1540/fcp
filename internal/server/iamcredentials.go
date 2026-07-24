package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"cloud.google.com/go/iam/credentials/apiv1/credentialspb"
	"github.com/devy1540/fcp/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type iamCredentialsServer struct {
	credentialspb.UnimplementedIAMCredentialsServer
	store *state.Store
}

func newIAMCredentialsServer(store *state.Store) *iamCredentialsServer {
	return &iamCredentialsServer{store: store}
}

func (s *iamCredentialsServer) GenerateAccessToken(_ context.Context, request *credentialspb.GenerateAccessTokenRequest) (*credentialspb.GenerateAccessTokenResponse, error) {
	if request.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "service account name is required")
	}
	expires := time.Now().UTC().Add(time.Hour)
	claims := map[string]any{
		"sub": request.GetName(), "scope": strings.Join(request.GetScope(), " "),
		"iat": time.Now().UTC().Unix(), "exp": expires.Unix(), "iss": "fcp",
	}
	token, err := s.signJWT(request.GetName(), claims)
	if err != nil {
		return nil, err
	}
	return &credentialspb.GenerateAccessTokenResponse{AccessToken: token, ExpireTime: timestamppb.New(expires)}, nil
}

func (s *iamCredentialsServer) GenerateIdToken(_ context.Context, request *credentialspb.GenerateIdTokenRequest) (*credentialspb.GenerateIdTokenResponse, error) {
	if request.GetName() == "" || request.GetAudience() == "" {
		return nil, status.Error(codes.InvalidArgument, "service account name and audience are required")
	}
	now := time.Now().UTC()
	claims := map[string]any{
		"sub": request.GetName(), "aud": request.GetAudience(), "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "iss": "fcp",
	}
	if request.GetIncludeEmail() {
		claims["email"] = strings.TrimPrefix(request.GetName(), "projects/-/serviceAccounts/")
		claims["email_verified"] = true
	}
	token, err := s.signJWT(request.GetName(), claims)
	if err != nil {
		return nil, err
	}
	return &credentialspb.GenerateIdTokenResponse{Token: token}, nil
}

func (s *iamCredentialsServer) SignBlob(_ context.Context, request *credentialspb.SignBlobRequest) (*credentialspb.SignBlobResponse, error) {
	if request.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "service account name is required")
	}
	account, privateKey, err := s.serviceAccount(request.GetName())
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(request.GetPayload())
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &credentialspb.SignBlobResponse{KeyId: account.KeyID, SignedBlob: signature}, nil
}

func (s *iamCredentialsServer) SignJwt(_ context.Context, request *credentialspb.SignJwtRequest) (*credentialspb.SignJwtResponse, error) {
	if request.GetName() == "" || request.GetPayload() == "" {
		return nil, status.Error(codes.InvalidArgument, "service account name and payload are required")
	}
	var claims map[string]any
	if err := json.Unmarshal([]byte(request.GetPayload()), &claims); err != nil {
		return nil, status.Error(codes.InvalidArgument, "payload must be a JSON object")
	}
	account, _, err := s.serviceAccount(request.GetName())
	if err != nil {
		return nil, err
	}
	token, err := s.signJWT(request.GetName(), claims)
	if err != nil {
		return nil, err
	}
	return &credentialspb.SignJwtResponse{KeyId: account.KeyID, SignedJwt: token}, nil
}

func (s *iamCredentialsServer) signJWT(name string, claims map[string]any) (string, error) {
	account, privateKey, err := s.serviceAccount(name)
	if err != nil {
		return "", err
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": account.KeyID})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", status.Error(codes.Internal, err.Error())
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (s *iamCredentialsServer) serviceAccount(name string) (state.IAMServiceAccount, *rsa.PrivateKey, error) {
	account, err := s.store.IAMServiceAccount(name, func() ([]byte, error) {
		privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		return x509.MarshalPKCS8PrivateKey(privateKey)
	})
	if err != nil {
		return state.IAMServiceAccount{}, nil, status.Error(codes.Internal, err.Error())
	}
	parsed, err := x509.ParsePKCS8PrivateKey(account.PrivateKey)
	if err != nil {
		return state.IAMServiceAccount{}, nil, status.Error(codes.Internal, err.Error())
	}
	privateKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return state.IAMServiceAccount{}, nil, status.Error(codes.Internal, "service account key is not RSA")
	}
	return account, privateKey, nil
}
