# 호환성 범위

이 문서는 FCP가 검증한 동작과 아직 구현하지 않은 동작을 구분합니다. `FULL`은 적힌 범위의 요청·응답과 핵심 상태 전환을 테스트했다는 의미이고, `PARTIAL`은 명시된 제약이 있다는 의미입니다. 어느 등급도 실제 클라우드 전체와의 완전한 동일성을 보장하지 않습니다.

이 파일의 서비스별 표는 `internal/compatibility` 카탈로그에서 생성됩니다. 변경 후 `make compatibility`로 갱신하며 테스트가 문서 드리프트를 검사합니다.

## 로컬 관리 대시보드

`/_fcp/ui`에서 AWS와 GCP를 필터링하고 서비스별 런타임 상태, 검증 등급, 지원 동작과 제외 범위를 확인할 수 있습니다. `READY`는 현재 프로세스가 요청에 응답할 수 있다는 뜻이며 전체 클라우드 동등성을 뜻하지 않습니다. 대시보드는 Secret payload, DynamoDB item, key material, 메시지 본문, AI 프롬프트와 생성 결과를 노출하지 않습니다.

## 서비스별 검증 매트릭스

### AWS · S3

- 검증 방식: `공식 SDK 검증`
- 검증 근거: AWS SDK JavaScript v3.1092.0

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| Create/Head/List/DeleteBucket | FULL | 생성, 조회, 정렬, 빈 버킷 삭제와 BucketNotEmpty |
| Put/Get/Head/DeleteObject | FULL | 본문, 메타데이터, ETag, Range GET와 영속화 |
| CopyObject | PARTIAL | 버킷 간 COPY/REPLACE; 조건부 copy와 version ID 제외 |
| Multipart upload | PARTIAL | 생성, part, 목록, 완료·중단과 재시작 영속화 |
| ListObjectsV2 | PARTIAL | prefix, max-keys, start-after, continuation-token |
| Bucket notifications | PARTIAL | SQS 대상 ObjectCreated Put과 prefix/suffix 필터 |
| Presigned GET | PARTIAL | SDK URL 생성과 다운로드; 서명·만료 검증 제외 |

제외 범위:
- ACL·IAM 정책·버전 관리·수명 주기는 구현하지 않습니다.
- 실제 S3의 분산 일관성과 성능 특성은 재현하지 않습니다.

### AWS · SQS

- 검증 방식: `공식 SDK 검증`
- 검증 근거: AWS SDK JavaScript v3.1092.0 · Java v2.33.9

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| Create/Get/List/DeleteQueue | FULL | Standard 큐 생성, 조회와 삭제 |
| Send/Receive/DeleteMessage | FULL | 본문, MD5, receipt handle와 메시지 속성 |
| Batch send/delete | PARTIAL | 성공·실패 항목 구분 |
| Visibility/Purge | FULL | visibility 변경과 전체 메시지 제거 |
| Queue attributes | PARTIAL | 주요 설정과 대략적인 메시지 수 |
| DLQ redrive | PARTIAL | maxReceiveCount 검증과 기존 DLQ 이동 |
| FIFO queue | PARTIAL | 그룹 순서, 5분 dedup, batch와 재시작 영속화 |
| Long polling/DelaySeconds | PARTIAL | 범위 내 대기와 Standard 메시지 지연 |

제외 범위:
- Queue policy·IAM·KMS와 AWS 처리량 quota는 구현하지 않습니다.
- 분산 중복 전달 확률과 fair queue scheduling은 재현하지 않습니다.

### AWS · DynamoDB

- 검증 방식: `공식 SDK 검증`
- 검증 근거: AWS SDK Java v2.33.9

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| Create/Describe/List/DeleteTable | FULL | HASH/RANGE 키, PAY_PER_REQUEST와 즉시 ACTIVE |
| Put/Get/DeleteItem | PARTIAL | String·Number 중심 값, 조건식과 ReturnValues |
| BatchGet/BatchWriteItem | PARTIAL | 요청 제한, 여러 테이블과 projection |
| Query/Scan | PARTIAL | 조건, 필터, projection, limit와 page key |
| UpdateItem | PARTIAL | SET/REMOVE/ADD와 주요 조건식 |
| TransactWriteItems | PARTIAL | 최대 100개 Put/Delete의 단일 프로세스 원자 적용 |

제외 범위:
- Secondary index·Streams·TTL·backup·throttling은 구현하지 않습니다.
- 고급 condition function과 PartiQL은 구현하지 않습니다.

### AWS · STS

- 검증 방식: `공식 SDK 검증`
- 검증 근거: AWS SDK Java v2.33.9

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| GetCallerIdentity | FULL | 고정 로컬 account와 user ARN을 Query XML로 반환 |

제외 범위:
- AssumeRole과 실제 토큰 발급은 구현하지 않습니다.
- 계정 ID와 리전은 로컬 고정값입니다.

### GCP · Cloud Storage

- 검증 방식: `공식 SDK 검증`
- 검증 근거: Storage Go v1.64.0 · Java v2.68.0 · JavaScript v7.21.0

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| Buckets insert/get/list/delete | FULL | 프로젝트별 목록, 위치, storage class와 빈 버킷 삭제 |
| Objects insert multipart | FULL | 본문, Content-Type, metadata와 checksum 응답 |
| Objects insert resumable | PARTIAL | 다중 청크; 재시작 재개와 중복 청크 재시도 제외 |
| Objects get/download | FULL | 메타데이터, 전체·Range 본문과 checksum 헤더 |
| Objects list/patch/delete | PARTIAL | prefix, pageToken, metadata 변경과 삭제 |
| V4 signed GET/HEAD/PUT | FULL | 만료, signed headers, signBlob/로컬 서명과 변조 차단 |
| V4 signed POST policy | PARTIAL | exact, starts-with와 content-length-range 조건 |

제외 범위:
- Object versioning·compose/copy/rewrite·retention은 구현하지 않습니다.
- ACL·IAM 정책과 실제 GCS의 분산 특성은 재현하지 않습니다.

### GCP · Pub/Sub

- 검증 방식: `공식 SDK 검증`
- 검증 근거: Pub/Sub Go v2.6.1 · Java v1.140.1 · JavaScript v5.3.1

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| Topic CRUD/list | FULL | 네이티브 gRPC topic 관리와 labels |
| Subscription CRUD/list | FULL | Pull subscription과 ack deadline |
| Publish | FULL | 데이터, attributes, ordering key와 message ID |
| Pull/StreamingPull | PARTIAL | 수신, visibility와 재전달; 고급 flow control 제외 |
| Acknowledge/ModifyAckDeadline | FULL | ack 삭제, deadline 연장과 즉시 재노출 |
| ListTopicSubscriptions | FULL | Topic별 subscription 목록 |
| UpdateSubscription | PARTIAL | ack deadline, labels와 dead-letter field mask |
| Dead-letter forwarding | PARTIAL | 최대 전달 횟수, deliveryAttempt와 NACK 후 전달 |

제외 범위:
- Push 전달·exactly-once·snapshot/seek·schema는 구현하지 않습니다.
- 다중 노드 ordering 보장은 재현하지 않습니다.

### GCP · Firestore

- 검증 방식: `공식 SDK 검증`
- 검증 근거: Firestore Go v1.24.0 · Kotlin/Java

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| Document CRUD/list | FULL | 문서 생성, 조회, 갱신, 삭제와 목록 |
| Batch get/write/commit | FULL | 여러 문서 읽기와 commit |
| Transactions | PARTIAL | begin과 rollback; 분산 충돌 제외 |
| Structured query | PARTIAL | 조건, 정렬, cursor, limit, offset와 projection |
| Field transforms | FULL | increment, server timestamp, array union/remove와 delete field |

제외 범위:
- 복합 인덱스 요구·보안 규칙·watch/listen은 재현하지 않습니다.
- Aggregation과 실제 분산 transaction 충돌은 구현하지 않습니다.

### GCP · Secret Manager

- 검증 방식: `공식 SDK 검증`
- 검증 근거: Secret Manager Go v1.21.0 · Java v2.52.0/v2.59.0

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| Secret CRUD/list | FULL | Secret 생성, 조회, 목록, 갱신과 삭제 |
| Version add/list/access | FULL | 버전과 latest 접근, CRC32C |
| Version state | FULL | enable, disable와 destroy |
| HTTP JSON access | FULL | versions/*:access 요청 경로 |

제외 범위:
- IAM 정책과 복제 리전 동작은 구현하지 않습니다.
- 대시보드와 로그는 Secret payload를 노출하지 않습니다.

### GCP · Cloud KMS

- 검증 방식: `공식 SDK 검증`
- 검증 근거: Cloud KMS Go v1.31.0 · Java v2.96.0

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| Key ring/key/version | FULL | 로컬 key lifecycle과 상태 |
| Encrypt/decrypt | FULL | AES-GCM과 HTTP JSON 요청 경로 |
| Asymmetric sign/public key | FULL | RSA PKCS#1 SHA-256 서명과 공개키 |

제외 범위:
- HSM·IAM·audit 특성은 재현하지 않습니다.
- 키 material은 FCP data directory 밖으로 반환하지 않습니다.

### GCP · IAM Credentials

- 검증 방식: `공식 SDK 검증`
- 검증 근거: IAM Credentials Go v1.12.0 · Java v2.51.0

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| GenerateAccessToken | FULL | 로컬 OAuth access token |
| GenerateIdToken | FULL | audience 기반 로컬 identity token |
| SignBlob/SignJwt | FULL | 로컬 서비스 계정 key 서명 |

제외 범위:
- 실제 Google IAM 권한 평가는 수행하지 않습니다.
- 개인키는 API와 대시보드에 노출하지 않습니다.

### GCP · FCM

- 검증 방식: `HTTP 계약 검증`
- 검증 근거: FCM HTTP v1 요청 경로

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| messages:send | FULL | 외부 발송 없이 HTTP v1 요청을 영속 캡처 |
| Deterministic errors | FULL | fcp-error-unregistered* NOT_FOUND 재현 |

제외 범위:
- Firebase 실제 전달과 APNs·Android 플랫폼 동작은 재현하지 않습니다.
- 메시지 본문은 대시보드에 노출하지 않습니다.

### GCP · Compute Metadata

- 검증 방식: `HTTP 계약 검증`
- 검증 근거: Metadata REST · JWKS 경로

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| Project ID/service account | FULL | Metadata-Flavor 계약의 로컬 identity |
| OAuth access token | FULL | 기본 서비스 계정 token 응답 |
| Identity token | FULL | audience 기반 JWT와 로컬 JWKS 검증 |
| JWKS | FULL | /oauth2/v3/certs 공개키 응답 |

제외 범위:
- GCE 인스턴스·네트워크·startup script 속성은 구현하지 않습니다.
- 실제 Google IAM 권한은 재현하지 않습니다.

### GCP · Vertex AI

- 검증 방식: `공식 SDK 검증`
- 검증 근거: Google Gen AI Java v1.58.0

| API/동작 | 상태 | 검증 범위 |
|---|---|---|
| Model list | FULL | Vertex publisher와 Gemini Developer API 목록 |
| generateContent | FULL | 고정 텍스트·JSON 응답과 호출 메타데이터 |
| streamGenerateContent | FULL | 결정적인 스트리밍 응답 |
| Deterministic errors | FULL | 429 rate-limit와 503 unavailable 재현 |

제외 범위:
- 실제 모델 의미론·멀티모달·safety 정책은 재현하지 않습니다.
- 프롬프트와 생성 결과 본문은 저장하지 않습니다.

## 공통 차이와 실행 안전성

- AWS 계정 ID는 `000000000000`, 리전은 `us-east-1`로 고정됩니다.
- SigV4 서명과 자격 증명은 검증하지 않습니다.
- 메타데이터는 JSON snapshot, 객체 본문은 별도 로컬 파일에 저장합니다.
- 동일 데이터 디렉터리는 한 프로세스만 열 수 있으며 두 번째 writer는 시작 단계에서 거부됩니다.
- `startup` 무결성 모드는 시작 시 객체 SHA-256을 검사하고, 선택적인 `strict` 모드는 실행 중 객체 읽기와 스냅샷 저장 시 다시 검사합니다.
- 다중 노드·리전 장애, 실제 서비스의 성능·quota·분산 일관성은 재현하지 않습니다.
