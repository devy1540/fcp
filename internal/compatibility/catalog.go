package compatibility

import (
	"errors"
	"fmt"
	"slices"
)

const Source = "docs/compatibility.md"

type Service struct {
	ID          string
	Name        string
	Provider    string
	Description string
	Level       string
	Evidence    string
	Operations  []Operation
	Limitations []string
}

type Operation struct {
	Name   string
	Status string
	Scope  string
}

func Services() []Service {
	services := slices.Clone(catalog)
	for i := range services {
		services[i].Operations = slices.Clone(services[i].Operations)
		services[i].Limitations = slices.Clone(services[i].Limitations)
	}
	return services
}

func VerificationLabel(level string) string {
	if level == "CONTRACT" {
		return "HTTP 계약 검증"
	}
	return "공식 SDK 검증"
}

func Validate(services []Service) error {
	if len(services) == 0 {
		return errors.New("compatibility catalog is empty")
	}
	seen := make(map[string]struct{}, len(services))
	for _, service := range services {
		if service.ID == "" || service.Name == "" || service.Description == "" || service.Evidence == "" {
			return fmt.Errorf("service %q is missing metadata", service.ID)
		}
		if service.Provider != "AWS" && service.Provider != "GCP" {
			return fmt.Errorf("service %q has invalid provider %q", service.ID, service.Provider)
		}
		if service.Level != "SDK" && service.Level != "CONTRACT" {
			return fmt.Errorf("service %q has invalid verification level %q", service.ID, service.Level)
		}
		if _, exists := seen[service.ID]; exists {
			return fmt.Errorf("duplicate service ID %q", service.ID)
		}
		seen[service.ID] = struct{}{}
		if len(service.Operations) == 0 {
			return fmt.Errorf("service %q has no verified operations", service.ID)
		}
		for _, operation := range service.Operations {
			if operation.Name == "" || operation.Scope == "" {
				return fmt.Errorf("service %q has an incomplete operation", service.ID)
			}
			if operation.Status != "FULL" && operation.Status != "PARTIAL" {
				return fmt.Errorf("service %q operation %q has invalid status %q", service.ID, operation.Name, operation.Status)
			}
		}
	}
	return nil
}

func operation(name, status, scope string) Operation {
	return Operation{Name: name, Status: status, Scope: scope}
}

var catalog = []Service{
	{
		ID: "s3", Name: "S3", Provider: "AWS", Description: "버킷과 객체 저장 상태",
		Level: "SDK", Evidence: "AWS SDK JavaScript v3.1092.0",
		Operations: []Operation{
			operation("Create/Head/List/DeleteBucket", "FULL", "생성, 조회, 정렬, 빈 버킷 삭제와 BucketNotEmpty"),
			operation("Put/Get/Head/DeleteObject", "FULL", "본문, 메타데이터, ETag, Range GET와 영속화"),
			operation("CopyObject", "PARTIAL", "버킷 간 COPY/REPLACE; 조건부 copy와 version ID 제외"),
			operation("Multipart upload", "PARTIAL", "생성, part, 목록, 완료·중단과 재시작 영속화"),
			operation("ListObjectsV2", "PARTIAL", "prefix, max-keys, start-after, continuation-token"),
			operation("Bucket notifications", "PARTIAL", "SQS 대상 ObjectCreated Put과 prefix/suffix 필터"),
			operation("Presigned GET", "PARTIAL", "SDK URL 생성과 다운로드; 서명·만료 검증 제외"),
		},
		Limitations: []string{"ACL·IAM 정책·버전 관리·수명 주기는 구현하지 않습니다.", "실제 S3의 분산 일관성과 성능 특성은 재현하지 않습니다."},
	},
	{
		ID: "sqs", Name: "SQS", Provider: "AWS", Description: "큐와 대기 메시지 상태",
		Level: "SDK", Evidence: "AWS SDK JavaScript v3.1092.0 · Java v2.33.9",
		Operations: []Operation{
			operation("Create/Get/List/DeleteQueue", "FULL", "Standard 큐 생성, 조회와 삭제"),
			operation("Send/Receive/DeleteMessage", "FULL", "본문, MD5, receipt handle와 메시지 속성"),
			operation("Batch send/delete", "PARTIAL", "성공·실패 항목 구분"),
			operation("Visibility/Purge", "FULL", "visibility 변경과 전체 메시지 제거"),
			operation("Queue attributes", "PARTIAL", "주요 설정과 대략적인 메시지 수"),
			operation("DLQ redrive", "PARTIAL", "maxReceiveCount 검증과 기존 DLQ 이동"),
			operation("FIFO queue", "PARTIAL", "그룹 순서, 5분 dedup, batch와 재시작 영속화"),
			operation("Long polling/DelaySeconds", "PARTIAL", "범위 내 대기와 Standard 메시지 지연"),
		},
		Limitations: []string{"Queue policy·IAM·KMS와 AWS 처리량 quota는 구현하지 않습니다.", "분산 중복 전달 확률과 fair queue scheduling은 재현하지 않습니다."},
	},
	{
		ID: "dynamodb", Name: "DynamoDB", Provider: "AWS", Description: "테이블 스키마와 저장 아이템 상태",
		Level: "SDK", Evidence: "AWS SDK Java v2.33.9",
		Operations: []Operation{
			operation("Create/Describe/List/DeleteTable", "FULL", "HASH/RANGE 키, PAY_PER_REQUEST와 즉시 ACTIVE"),
			operation("Put/Get/DeleteItem", "PARTIAL", "String·Number 중심 값, 조건식과 ReturnValues"),
			operation("BatchGet/BatchWriteItem", "PARTIAL", "요청 제한, 여러 테이블과 projection"),
			operation("Query/Scan", "PARTIAL", "조건, 필터, projection, limit와 page key"),
			operation("UpdateItem", "PARTIAL", "SET/REMOVE/ADD와 주요 조건식"),
			operation("TransactWriteItems", "PARTIAL", "최대 100개 Put/Delete의 단일 프로세스 원자 적용"),
		},
		Limitations: []string{"Secondary index·Streams·TTL·backup·throttling은 구현하지 않습니다.", "고급 condition function과 PartiQL은 구현하지 않습니다."},
	},
	{
		ID: "sts", Name: "STS", Provider: "AWS", Description: "로컬 AWS 호출자 identity",
		Level: "SDK", Evidence: "AWS SDK Java v2.33.9",
		Operations: []Operation{
			operation("GetCallerIdentity", "FULL", "고정 로컬 account와 user ARN을 Query XML로 반환"),
		},
		Limitations: []string{"AssumeRole과 실제 토큰 발급은 구현하지 않습니다.", "계정 ID와 리전은 로컬 고정값입니다."},
	},
	{
		ID: "gcs", Name: "Cloud Storage", Provider: "GCP", Description: "버킷과 객체 저장 상태",
		Level: "SDK", Evidence: "Storage Go v1.64.0 · Java v2.68.0 · JavaScript v7.21.0",
		Operations: []Operation{
			operation("Buckets insert/get/list/delete", "FULL", "프로젝트별 목록, 위치, storage class와 빈 버킷 삭제"),
			operation("Objects insert multipart", "FULL", "본문, Content-Type, metadata와 checksum 응답"),
			operation("Objects insert resumable", "PARTIAL", "다중 청크; 재시작 재개와 중복 청크 재시도 제외"),
			operation("Objects get/download", "FULL", "메타데이터, 전체·Range 본문과 checksum 헤더"),
			operation("Objects list/patch/delete", "PARTIAL", "prefix, pageToken, metadata 변경과 삭제"),
			operation("V4 signed GET/HEAD/PUT", "FULL", "만료, signed headers, signBlob/로컬 서명과 변조 차단"),
			operation("V4 signed POST policy", "PARTIAL", "exact, starts-with와 content-length-range 조건"),
		},
		Limitations: []string{"Object versioning·compose/copy/rewrite·retention은 구현하지 않습니다.", "ACL·IAM 정책과 실제 GCS의 분산 특성은 재현하지 않습니다."},
	},
	{
		ID: "pubsub", Name: "Pub/Sub", Provider: "GCP", Description: "토픽, 구독과 미확인 메시지",
		Level: "SDK", Evidence: "Pub/Sub Go v2.6.1 · Java v1.140.1 · JavaScript v5.3.1",
		Operations: []Operation{
			operation("Topic CRUD/list", "FULL", "네이티브 gRPC topic 관리와 labels"),
			operation("Subscription CRUD/list", "FULL", "Pull subscription과 ack deadline"),
			operation("Publish", "FULL", "데이터, attributes, ordering key와 message ID"),
			operation("Pull/StreamingPull", "PARTIAL", "수신, visibility와 재전달; 고급 flow control 제외"),
			operation("Acknowledge/ModifyAckDeadline", "FULL", "ack 삭제, deadline 연장과 즉시 재노출"),
			operation("ListTopicSubscriptions", "FULL", "Topic별 subscription 목록"),
			operation("UpdateSubscription", "PARTIAL", "ack deadline, labels와 dead-letter field mask"),
			operation("Dead-letter forwarding", "PARTIAL", "최대 전달 횟수, deliveryAttempt와 NACK 후 전달"),
		},
		Limitations: []string{"Push 전달·exactly-once·snapshot/seek·schema는 구현하지 않습니다.", "다중 노드 ordering 보장은 재현하지 않습니다."},
	},
	{
		ID: "firestore", Name: "Firestore", Provider: "GCP", Description: "저장된 문서 메타데이터",
		Level: "SDK", Evidence: "Firestore Go v1.24.0 · Kotlin/Java",
		Operations: []Operation{
			operation("Document CRUD/list", "FULL", "문서 생성, 조회, 갱신, 삭제와 목록"),
			operation("Batch get/write/commit", "FULL", "여러 문서 읽기와 commit"),
			operation("Transactions", "PARTIAL", "begin과 rollback; 분산 충돌 제외"),
			operation("Structured query", "PARTIAL", "조건, 정렬, cursor, limit, offset와 projection"),
			operation("Field transforms", "FULL", "increment, server timestamp, array union/remove와 delete field"),
		},
		Limitations: []string{"복합 인덱스 요구·보안 규칙·watch/listen은 재현하지 않습니다.", "Aggregation과 실제 분산 transaction 충돌은 구현하지 않습니다."},
	},
	{
		ID: "secrets", Name: "Secret Manager", Provider: "GCP", Description: "값을 제외한 Secret과 버전 상태",
		Level: "SDK", Evidence: "Secret Manager Go v1.21.0 · Java v2.52.0/v2.59.0",
		Operations: []Operation{
			operation("Secret CRUD/list", "FULL", "Secret 생성, 조회, 목록, 갱신과 삭제"),
			operation("Version add/list/access", "FULL", "버전과 latest 접근, CRC32C"),
			operation("Version state", "FULL", "enable, disable와 destroy"),
			operation("HTTP JSON access", "FULL", "versions/*:access 요청 경로"),
		},
		Limitations: []string{"IAM 정책과 복제 리전 동작은 구현하지 않습니다.", "대시보드와 로그는 Secret payload를 노출하지 않습니다."},
	},
	{
		ID: "kms", Name: "Cloud KMS", Provider: "GCP", Description: "키링, 키와 버전 상태",
		Level: "SDK", Evidence: "Cloud KMS Go v1.31.0 · Java v2.96.0",
		Operations: []Operation{
			operation("Key ring/key/version", "FULL", "로컬 key lifecycle과 상태"),
			operation("Encrypt/decrypt", "FULL", "AES-GCM과 HTTP JSON 요청 경로"),
			operation("Asymmetric sign/public key", "FULL", "RSA PKCS#1 SHA-256 서명과 공개키"),
		},
		Limitations: []string{"HSM·IAM·audit 특성은 재현하지 않습니다.", "키 material은 FCP data directory 밖으로 반환하지 않습니다."},
	},
	{
		ID: "iam", Name: "IAM Credentials", Provider: "GCP", Description: "개인키를 제외한 로컬 서비스 계정",
		Level: "SDK", Evidence: "IAM Credentials Go v1.12.0 · Java v2.51.0",
		Operations: []Operation{
			operation("GenerateAccessToken", "FULL", "로컬 OAuth access token"),
			operation("GenerateIdToken", "FULL", "audience 기반 로컬 identity token"),
			operation("SignBlob/SignJwt", "FULL", "로컬 서비스 계정 key 서명"),
		},
		Limitations: []string{"실제 Google IAM 권한 평가는 수행하지 않습니다.", "개인키는 API와 대시보드에 노출하지 않습니다."},
	},
	{
		ID: "fcm", Name: "FCM", Provider: "GCP", Description: "외부 발송 없이 캡처된 요청",
		Level: "CONTRACT", Evidence: "FCM HTTP v1 요청 경로",
		Operations: []Operation{
			operation("messages:send", "FULL", "외부 발송 없이 HTTP v1 요청을 영속 캡처"),
			operation("Deterministic errors", "FULL", "fcp-error-unregistered* NOT_FOUND 재현"),
		},
		Limitations: []string{"Firebase 실제 전달과 APNs·Android 플랫폼 동작은 재현하지 않습니다.", "메시지 본문은 대시보드에 노출하지 않습니다."},
	},
	{
		ID: "metadata", Name: "Compute Metadata", Provider: "GCP", Description: "로컬 프로젝트와 서비스 계정 identity",
		Level: "CONTRACT", Evidence: "Metadata REST · JWKS 경로",
		Operations: []Operation{
			operation("Project ID/service account", "FULL", "Metadata-Flavor 계약의 로컬 identity"),
			operation("OAuth access token", "FULL", "기본 서비스 계정 token 응답"),
			operation("Identity token", "FULL", "audience 기반 JWT와 로컬 JWKS 검증"),
			operation("JWKS", "FULL", "/oauth2/v3/certs 공개키 응답"),
		},
		Limitations: []string{"GCE 인스턴스·네트워크·startup script 속성은 구현하지 않습니다.", "실제 Google IAM 권한은 재현하지 않습니다."},
	},
	{
		ID: "vertex", Name: "Vertex AI", Provider: "GCP", Description: "모델 목록과 로컬 생성 호출 상태",
		Level: "SDK", Evidence: "Google Gen AI Java v1.58.0",
		Operations: []Operation{
			operation("Model list", "FULL", "Vertex publisher와 Gemini Developer API 목록"),
			operation("generateContent", "FULL", "고정 텍스트·JSON 응답과 호출 메타데이터"),
			operation("streamGenerateContent", "FULL", "결정적인 스트리밍 응답"),
			operation("Deterministic errors", "FULL", "429 rate-limit와 503 unavailable 재현"),
		},
		Limitations: []string{"실제 모델 의미론·멀티모달·safety 정책은 재현하지 않습니다.", "프롬프트와 생성 결과 본문은 저장하지 않습니다."},
	},
}
