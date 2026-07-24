package server

const dashboardVerificationSource = "docs/compatibility.md"

type dashboardVerificationSpec struct {
	operations  []dashboardOperationVerification
	limitations []string
}

func dashboardVerificationFor(serviceID, level, evidence string) dashboardVerification {
	spec := dashboardVerificationSpecs[serviceID]
	label := "공식 SDK 검증"
	if level == "CONTRACT" {
		label = "HTTP 계약 검증"
	}
	return dashboardVerification{
		Level: level, Label: label, Evidence: evidence, Source: dashboardVerificationSource,
		Operations: spec.operations, Limitations: spec.limitations,
	}
}

func verifiedOperation(name, status, scope string) dashboardOperationVerification {
	return dashboardOperationVerification{Name: name, Status: status, Scope: scope}
}

var dashboardVerificationSpecs = map[string]dashboardVerificationSpec{
	"s3": {
		operations: []dashboardOperationVerification{
			verifiedOperation("Create/Head/List/DeleteBucket", "FULL", "생성, 조회, 정렬, 빈 버킷 삭제와 BucketNotEmpty"),
			verifiedOperation("Put/Get/Head/DeleteObject", "FULL", "본문, 메타데이터, ETag, Range GET와 영속화"),
			verifiedOperation("CopyObject", "PARTIAL", "버킷 간 COPY/REPLACE; 조건부 copy와 version ID 제외"),
			verifiedOperation("Multipart upload", "PARTIAL", "생성, part, 목록, 완료·중단과 재시작 영속화"),
			verifiedOperation("ListObjectsV2", "PARTIAL", "prefix, max-keys, start-after, continuation-token"),
			verifiedOperation("Bucket notifications", "PARTIAL", "SQS 대상 ObjectCreated Put과 prefix/suffix 필터"),
			verifiedOperation("Presigned GET", "PARTIAL", "SDK URL 생성과 다운로드; 서명·만료 검증 제외"),
		},
		limitations: []string{"ACL·IAM 정책·버전 관리·수명 주기는 구현하지 않습니다.", "실제 S3의 분산 일관성과 성능 특성은 재현하지 않습니다."},
	},
	"sqs": {
		operations: []dashboardOperationVerification{
			verifiedOperation("Create/Get/List/DeleteQueue", "FULL", "Standard 큐 생성, 조회와 삭제"),
			verifiedOperation("Send/Receive/DeleteMessage", "FULL", "본문, MD5, receipt handle와 메시지 속성"),
			verifiedOperation("Batch send/delete", "PARTIAL", "성공·실패 항목 구분"),
			verifiedOperation("Visibility/Purge", "FULL", "visibility 변경과 전체 메시지 제거"),
			verifiedOperation("Queue attributes", "PARTIAL", "주요 설정과 대략적인 메시지 수"),
			verifiedOperation("DLQ redrive", "PARTIAL", "maxReceiveCount 검증과 기존 DLQ 이동"),
			verifiedOperation("FIFO queue", "PARTIAL", "그룹 순서, 5분 dedup, batch와 재시작 영속화"),
			verifiedOperation("Long polling/DelaySeconds", "PARTIAL", "범위 내 대기와 Standard 메시지 지연"),
		},
		limitations: []string{"Queue policy·IAM·KMS와 AWS 처리량 quota는 구현하지 않습니다.", "분산 중복 전달 확률과 fair queue scheduling은 재현하지 않습니다."},
	},
	"dynamodb": {
		operations: []dashboardOperationVerification{
			verifiedOperation("Create/Describe/List/DeleteTable", "FULL", "HASH/RANGE 키, PAY_PER_REQUEST와 즉시 ACTIVE"),
			verifiedOperation("Put/Get/DeleteItem", "PARTIAL", "String·Number 중심 값, 조건식과 ReturnValues"),
			verifiedOperation("BatchGet/BatchWriteItem", "PARTIAL", "요청 제한, 여러 테이블과 projection"),
			verifiedOperation("Query/Scan", "PARTIAL", "조건, 필터, projection, limit와 page key"),
			verifiedOperation("UpdateItem", "PARTIAL", "SET/REMOVE/ADD와 주요 조건식"),
			verifiedOperation("TransactWriteItems", "PARTIAL", "최대 100개 Put/Delete의 단일 프로세스 원자 적용"),
		},
		limitations: []string{"Secondary index·Streams·TTL·backup·throttling은 구현하지 않습니다.", "고급 condition function과 PartiQL은 구현하지 않습니다."},
	},
	"sts": {
		operations: []dashboardOperationVerification{
			verifiedOperation("GetCallerIdentity", "FULL", "고정 로컬 account와 user ARN을 Query XML로 반환"),
		},
		limitations: []string{"AssumeRole과 실제 토큰 발급은 구현하지 않습니다.", "계정 ID와 리전은 로컬 고정값입니다."},
	},
	"gcs": {
		operations: []dashboardOperationVerification{
			verifiedOperation("Buckets insert/get/list/delete", "FULL", "프로젝트별 목록, 위치, storage class와 빈 버킷 삭제"),
			verifiedOperation("Objects insert multipart", "FULL", "본문, Content-Type, metadata와 checksum 응답"),
			verifiedOperation("Objects insert resumable", "PARTIAL", "다중 청크; 재시작 재개와 중복 청크 재시도 제외"),
			verifiedOperation("Objects get/download", "FULL", "메타데이터, 전체·Range 본문과 checksum 헤더"),
			verifiedOperation("Objects list/patch/delete", "PARTIAL", "prefix, pageToken, metadata 변경과 삭제"),
			verifiedOperation("V4 signed GET/HEAD/PUT", "FULL", "만료, signed headers, signBlob/로컬 서명과 변조 차단"),
			verifiedOperation("V4 signed POST policy", "PARTIAL", "exact, starts-with와 content-length-range 조건"),
		},
		limitations: []string{"Object versioning·compose/copy/rewrite·retention은 구현하지 않습니다.", "ACL·IAM 정책과 실제 GCS의 분산 특성은 재현하지 않습니다."},
	},
	"pubsub": {
		operations: []dashboardOperationVerification{
			verifiedOperation("Topic CRUD/list", "FULL", "네이티브 gRPC topic 관리와 labels"),
			verifiedOperation("Subscription CRUD/list", "FULL", "Pull subscription과 ack deadline"),
			verifiedOperation("Publish", "FULL", "데이터, attributes, ordering key와 message ID"),
			verifiedOperation("Pull/StreamingPull", "PARTIAL", "수신, visibility와 재전달; 고급 flow control 제외"),
			verifiedOperation("Acknowledge/ModifyAckDeadline", "FULL", "ack 삭제, deadline 연장과 즉시 재노출"),
			verifiedOperation("ListTopicSubscriptions", "FULL", "Topic별 subscription 목록"),
			verifiedOperation("UpdateSubscription", "PARTIAL", "ack deadline, labels와 dead-letter field mask"),
			verifiedOperation("Dead-letter forwarding", "PARTIAL", "최대 전달 횟수, deliveryAttempt와 NACK 후 전달"),
		},
		limitations: []string{"Push 전달·exactly-once·snapshot/seek·schema는 구현하지 않습니다.", "다중 노드 ordering 보장은 재현하지 않습니다."},
	},
	"firestore": {
		operations: []dashboardOperationVerification{
			verifiedOperation("Document CRUD/list", "FULL", "문서 생성, 조회, 갱신, 삭제와 목록"),
			verifiedOperation("Batch get/write/commit", "FULL", "여러 문서 읽기와 commit"),
			verifiedOperation("Transactions", "PARTIAL", "begin과 rollback; 분산 충돌 제외"),
			verifiedOperation("Structured query", "PARTIAL", "조건, 정렬, cursor, limit, offset와 projection"),
			verifiedOperation("Field transforms", "FULL", "increment, server timestamp, array union/remove와 delete field"),
		},
		limitations: []string{"복합 인덱스 요구·보안 규칙·watch/listen은 재현하지 않습니다.", "Aggregation과 실제 분산 transaction 충돌은 구현하지 않습니다."},
	},
	"secrets": {
		operations: []dashboardOperationVerification{
			verifiedOperation("Secret CRUD/list", "FULL", "Secret 생성, 조회, 목록, 갱신과 삭제"),
			verifiedOperation("Version add/list/access", "FULL", "버전과 latest 접근, CRC32C"),
			verifiedOperation("Version state", "FULL", "enable, disable와 destroy"),
			verifiedOperation("HTTP JSON access", "FULL", "versions/*:access 요청 경로"),
		},
		limitations: []string{"IAM 정책과 복제 리전 동작은 구현하지 않습니다.", "대시보드와 로그는 Secret payload를 노출하지 않습니다."},
	},
	"kms": {
		operations: []dashboardOperationVerification{
			verifiedOperation("Key ring/key/version", "FULL", "로컬 key lifecycle과 상태"),
			verifiedOperation("Encrypt/decrypt", "FULL", "AES-GCM과 HTTP JSON 요청 경로"),
			verifiedOperation("Asymmetric sign/public key", "FULL", "RSA PKCS#1 SHA-256 서명과 공개키"),
		},
		limitations: []string{"HSM·IAM·audit 특성은 재현하지 않습니다.", "키 material은 FCP data directory 밖으로 반환하지 않습니다."},
	},
	"iam": {
		operations: []dashboardOperationVerification{
			verifiedOperation("GenerateAccessToken", "FULL", "로컬 OAuth access token"),
			verifiedOperation("GenerateIdToken", "FULL", "audience 기반 로컬 identity token"),
			verifiedOperation("SignBlob/SignJwt", "FULL", "로컬 서비스 계정 key 서명"),
		},
		limitations: []string{"실제 Google IAM 권한 평가는 수행하지 않습니다.", "개인키는 API와 대시보드에 노출하지 않습니다."},
	},
	"fcm": {
		operations: []dashboardOperationVerification{
			verifiedOperation("messages:send", "FULL", "외부 발송 없이 HTTP v1 요청을 영속 캡처"),
			verifiedOperation("Deterministic errors", "FULL", "fcp-error-unregistered* NOT_FOUND 재현"),
		},
		limitations: []string{"Firebase 실제 전달과 APNs·Android 플랫폼 동작은 재현하지 않습니다.", "메시지 본문은 대시보드에 노출하지 않습니다."},
	},
	"metadata": {
		operations: []dashboardOperationVerification{
			verifiedOperation("Project ID/service account", "FULL", "Metadata-Flavor 계약의 로컬 identity"),
			verifiedOperation("OAuth access token", "FULL", "기본 서비스 계정 token 응답"),
			verifiedOperation("Identity token", "FULL", "audience 기반 JWT와 로컬 JWKS 검증"),
			verifiedOperation("JWKS", "FULL", "/oauth2/v3/certs 공개키 응답"),
		},
		limitations: []string{"GCE 인스턴스·네트워크·startup script 속성은 구현하지 않습니다.", "실제 Google IAM 권한은 재현하지 않습니다."},
	},
	"vertex": {
		operations: []dashboardOperationVerification{
			verifiedOperation("Model list", "FULL", "Vertex publisher와 Gemini Developer API 목록"),
			verifiedOperation("generateContent", "FULL", "고정 텍스트·JSON 응답과 호출 메타데이터"),
			verifiedOperation("streamGenerateContent", "FULL", "결정적인 스트리밍 응답"),
			verifiedOperation("Deterministic errors", "FULL", "429 rate-limit와 503 unavailable 재현"),
		},
		limitations: []string{"실제 모델 의미론·멀티모달·safety 정책은 재현하지 않습니다.", "프롬프트와 생성 결과 본문은 저장하지 않습니다."},
	},
}
