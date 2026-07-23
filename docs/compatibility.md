# 호환성 범위

이 문서는 FCP가 검증한 동작과 아직 구현하지 않은 동작을 구분합니다. `FULL`은 아래에 적힌 범위 내에서 요청·응답과 핵심 상태 전환을 테스트했다는 의미이며 AWS 전체 동작과의 완전한 동일성을 의미하지 않습니다.

## 로컬 관리 대시보드

`/_fcp/ui`에서 AWS와 GCP를 필터링·그룹화하고 각 서비스의 런타임 상태와 검증 등급을 별도로 확인할 수 있습니다. `READY`는 현재 프로세스가 요청에 응답할 수 있음을 뜻합니다. `공식 SDK 검증`은 명시된 실제 클라이언트 버전의 회귀 테스트, `HTTP 계약 검증`은 PODO가 직접 호출하는 HTTP 경로 테스트를 뜻하며 둘 다 전체 클라우드 동등성을 의미하지 않습니다. 서비스 상세에서는 이 문서에 대응하는 API별 `FULL`·`PARTIAL` 범위와 제외 범위를 확인할 수 있습니다. 첫 화면은 서비스 요약만 받고 서비스 선택 시 리소스를 25개씩 지연 조회하며, 검색과 이전·다음 페이지는 서버에서 처리합니다. 화면은 표시 중일 때 3초마다 자동 갱신하고 마지막 성공 응답이 10초를 넘으면 `STALE`로 표시합니다. 서비스별 리소스를 조회·검색하고 S3의 진행 중 멀티파트 업로드 수, SQS의 Standard/FIFO 유형·content dedup·DLQ 대상·최대 수신 횟수, DynamoDB 테이블 스키마·아이템 수와 SQS, Pub/Sub, FCM 메시지 상태, Vertex AI 생성 호출 메타데이터를 확인할 수 있습니다. S3 버킷, SQS Standard/FIFO 큐, DynamoDB 테이블, Cloud Storage 버킷, Pub/Sub Topic·Subscription의 생성·삭제를 지원하며 입력은 서버에서도 검증합니다. 모든 삭제는 확인 단계를 거치고 S3와 Cloud Storage 버킷은 비어 있지 않으면 `409 Conflict`로 거부합니다. 테스트 데이터 초기화는 Storage 객체와 미완료 멀티파트 업로드, 메시지, DynamoDB 아이템, FIFO dedup 기록, Firestore 문서, FCM 캡처와 Vertex AI 호출 기록만 삭제하며 리소스 구조, Secret, KMS와 IAM key material은 보존합니다. 대시보드 응답에는 Secret payload, DynamoDB 아이템, key material, 업로드 파트, 메시지 본문, AI 프롬프트와 생성 결과를 포함하지 않습니다.

## S3

| API | 상태 | 검증 범위 |
|---|---|---|
| CreateBucket | FULL | 생성, 중복 생성 |
| HeadBucket | FULL | 존재 여부 |
| ListBuckets | FULL | 이름, 생성 시각, 정렬 |
| DeleteBucket | FULL | 빈 버킷 삭제, BucketNotEmpty |
| PutObject | FULL | 본문, Content-Type, 사용자 메타데이터, ETag, 영속화 |
| GetObject | FULL | 본문과 메타데이터 |
| GetObject Range | FULL | 단일·개방·suffix byte range, 206과 Content-Range |
| HeadObject | FULL | 크기, ETag, Last-Modified, 메타데이터 |
| DeleteObject | FULL | 삭제와 멱등성 |
| CopyObject | PARTIAL | 버킷 간 복사, Content-Type/metadata COPY 및 REPLACE. 조건부 copy와 version ID 미지원 |
| Multipart upload | PARTIAL | 생성, UploadPart, ListParts/ListMultipartUploads, 완료·중단, 재시작 영속화, multipart ETag와 비최종 파트 5 MiB 제한. UploadPartCopy, checksum 검증과 전체 목록 pagination 미지원 |
| ListObjectsV2 | PARTIAL | prefix, max-keys, start-after, continuation-token. delimiter 미지원 |
| Put/GetBucketNotificationConfiguration | PARTIAL | SQS 대상, ObjectCreated Put, prefix/suffix 필터 |
| Presigned GET | PARTIAL | AWS SDK v3 URL 생성과 다운로드. 서명·만료 검증 미지원 |

미지원: ACL, 정책/IAM, 버전 관리, presigned URL 서명·만료 검증, 암호화, 수명 주기, 웹사이트 호스팅, 실제 S3의 일관성·성능 특성.

## SQS

| API | 상태 | 검증 범위 |
|---|---|---|
| Create/GetQueueUrl/List/DeleteQueue | FULL | 표준 큐 생성·조회·삭제 |
| Send/Receive/DeleteMessage | FULL | 본문, body·message attribute MD5, receipt handle, 메시지 속성 |
| Send/DeleteMessageBatch | PARTIAL | 성공·실패 항목 구분. AWS의 전체 입력 검증 규칙은 미지원 |
| ChangeMessageVisibility | FULL | 메시지 단위 visibility 변경 |
| PurgeQueue | FULL | 전체 메시지 제거 |
| Get/SetQueueAttributes | PARTIAL | 주요 설정 및 대략적인 메시지 수 |
| DLQ redrive | PARTIAL | RedrivePolicy 검증·조회, maxReceiveCount 1~1000, 제한 초과 시 기존 DLQ로 이동, 본문·속성·message ID 보존. RedriveAllowPolicy와 수동 redrive task 미지원 |
| FIFO queue | PARTIAL | `.fifo`/FifoQueue 검증, MessageGroupId별 순서와 in-flight 차단, 명시적·본문 SHA-256 dedup 5분, SequenceNumber, 배치, 재시작 영속화, FIFO DLQ 연동. ReceiveRequestAttemptId와 처리량 quota 미지원 |
| Long polling | PARTIAL | WaitTimeSeconds 범위 내 대기. 취소·분산 노드 동작은 미지원 |
| DelaySeconds | PARTIAL | 큐 기본 지연과 Standard 메시지별 지연, FIFO 메시지별 양수 지연 거부. AWS의 전체 입력 검증 규칙은 미지원 |

미지원: FIFO ReceiveRequestAttemptId·처리량 quota, Standard fair queue scheduling, DLQ 수동 redrive task와 RedriveAllowPolicy, queue policy/IAM, KMS, 태그, legacy Query XML 프로토콜, AWS의 분산 시스템 성능·중복 전달 확률.

## 공통 차이

- 계정 ID는 `000000000000`, 리전은 `us-east-1`로 고정됩니다.
- SigV4 서명과 자격 증명을 검증하지 않습니다.
- 메타데이터는 JSON snapshot, 객체 본문은 별도 로컬 파일에 저장합니다.
- 한 프로세스 안에서 동작하며 다중 노드·리전 장애를 재현하지 않습니다.
- AWS 영역은 AWS CLI 2.x, JavaScript AWS SDK v3.1092.0과 PODO notification의 Java AWS SDK v2.33.9(DynamoDB·SQS·STS 동기/비동기 클라이언트)로 회귀 검증합니다.

## DynamoDB와 STS

| API | 상태 | 검증 범위 |
|---|---|---|
| Create/Describe/List/DeleteTable | FULL | HASH/RANGE String·Number·Binary key schema, PAY_PER_REQUEST, 즉시 ACTIVE 상태 |
| Put/Get/DeleteItem | PARTIAL | PODO 경로에서 쓰는 String·Number 중심 AttributeValue, 조건식, ReturnValues, 영속화 |
| BatchGetItem | PARTIAL | 최대 100개 key, 여러 테이블, projection, 누락 item 생략. throttling이 없어 unprocessed key는 반환하지 않음 |
| BatchWriteItem | PARTIAL | 최대 25개 Put/Delete, 여러 테이블, 단일 snapshot 저장. throttling이 없어 unprocessed item은 반환하지 않음 |
| Query | PARTIAL | partition key equality, sort key equality·비교·between·begins_with, AND 필터, projection, limit/page key, 정방향·역방향 |
| Scan | PARTIAL | AND 필터, projection, count, limit/page key |
| UpdateItem | PARTIAL | SET, if_not_exists, REMOVE, 숫자 ADD, attribute exists/equality 조건, ReturnValues |
| TransactWriteItems | PARTIAL | 최대 100개 Put/Delete의 단일 프로세스 원자 적용 |
| STS GetCallerIdentity | FULL | 고정 로컬 account/user ARN을 AWS Query XML 프로토콜로 반환 |

미지원: DynamoDB secondary index, PartiQL, Streams, TTL, backup, capacity/throttling, set ADD/DELETE와 고급 condition function, IAM 정책. STS AssumeRole·토큰 발급은 미지원입니다.

## GCP Cloud Storage

| API | 상태 | 검증 범위 |
|---|---|---|
| Buckets insert/get/list/delete | FULL | 프로젝트별 목록, 위치, storage class, 빈 버킷 삭제 |
| Objects insert (multipart) | FULL | 본문, Content-Type, metadata, checksum 응답 |
| Objects insert (resumable) | PARTIAL | 다중 청크 업로드. 프로세스 재시작 후 재개와 중복 청크 재시도 미지원 |
| Objects get/download | FULL | 메타데이터, 전체 본문, Range GET, checksum 헤더 |
| Objects list | PARTIAL | prefix, maxResults, pageToken. delimiter와 glob 미지원 |
| Objects patch/delete | PARTIAL | Content-Type과 사용자 메타데이터 변경, 삭제 |
| V4 signed GET/HEAD/PUT | FULL | 만료, signed headers, IAM signBlob 또는 로컬 service-account 서명, 경로 변조 차단 |
| V4 signed POST policy | PARTIAL | 정책 서명, exact/starts-with/content-length-range 조건. ACL 적용 미지원 |

미지원: ACL/IAM 정책, 조건부 generation 요청, checksum 입력 검증, object versioning, compose/copy/rewrite, lifecycle, retention/hold, Pub/Sub notification, 실제 GCS의 일관성·성능 특성.

검증 클라이언트는 Go Storage v1.64.0, Java Storage v2.68.0, PODO 앱 lockfile의 JavaScript Storage v7.19.0입니다.

## GCP Pub/Sub

| RPC | 상태 | 검증 범위 |
|---|---|---|
| Create/Get/List/DeleteTopic | FULL | 네이티브 gRPC topic 관리와 labels |
| Create/Get/List/DeleteSubscription | FULL | pull subscription과 ack deadline |
| Publish | FULL | 데이터, attributes, ordering key, message ID |
| Pull/StreamingPull | PARTIAL | unary/stream 수신, visibility와 재전달. 고급 flow control 미지원 |
| Acknowledge/ModifyAckDeadline | FULL | ack 삭제, deadline 연장과 즉시 재노출 |
| ListTopicSubscriptions | FULL | topic별 subscription 목록 |
| UpdateSubscription | PARTIAL | ack deadline, labels, dead_letter_policy field mask |
| Dead-letter forwarding | PARTIAL | 최대 전달 횟수, deliveryAttempt, NACK 후 DLQ 전달 |

미지원: push subscription 전달, filter/retry policy, exactly-once, snapshot/seek, schema, IAM 정책, configurable retention, 다중 노드 ordering 보장.

검증 클라이언트는 `cloud.google.com/go/pubsub/v2` v2.6.1, Java Pub/Sub v1.140.1, PODO 앱 lockfile의 JavaScript Pub/Sub v4.11.0이며, 고수준 Publisher와 StreamingPull 기반 Subscriber까지 통과합니다.

## Firestore

문서 CRUD/list, batch get/write, commit, transaction begin/rollback, equality/range/composite query, order/cursor/limit/offset/projection, increment/server timestamp/array union·remove/delete field를 지원합니다. 복합 인덱스 요구, 보안 규칙, watch/listen, aggregation과 실제 분산 transaction 충돌은 재현하지 않습니다. Go와 PODO notification의 Kotlin/Java Firestore SDK로 검증합니다.

## Secret Manager

Secret CRUD/list, version 추가/list/access, `latest`, enable/disable/destroy와 CRC32C를 지원합니다. gRPC SDK 외에 PODO Node 서버가 사용하는 HTTP/JSON `projects/*/secrets/*/versions/*:access`도 지원합니다. IAM 정책과 복제 리전 동작은 미지원입니다. Go, Java/Kotlin SDK와 JavaScript REST 호출로 검증합니다.

## Cloud KMS와 IAM Credentials

KMS key ring/key/version, AES-GCM encrypt/decrypt, RSA PKCS#1 SHA-256 sign/public key를 로컬 키로 제공합니다. PODO Node 서버가 사용하는 HTTP/JSON `:encrypt`와 `:decrypt`는 동일한 로컬 키 상태를 사용합니다. IAM Credentials는 access/id token, signBlob, signJwt를 제공합니다. 키는 FCP data directory 안에만 저장되며 HSM/IAM/audit 특성은 재현하지 않습니다.

## Compute Metadata

PODO 서버가 사용하는 프로젝트 ID, 기본 서비스 계정 이메일, OAuth access token, audience 기반 identity token을 `Metadata-Flavor: Google` 계약과 함께 제공합니다. identity token 검증용 로컬 JWKS는 `/oauth2/v3/certs`에서 제공합니다. GCE 인스턴스 속성, 네트워크, startup script와 실제 Google IAM 권한은 재현하지 않습니다.

## FCM과 Vertex AI

FCM HTTP v1 `messages:send`는 외부 발송 없이 요청을 영속 캡처합니다. `fcp-error-unregistered*` 토큰은 재현 가능한 NOT_FOUND 오류를 반환합니다. Firebase의 전달·APNs/Android 플랫폼 동작은 미지원입니다.

Vertex AI publisher model list와 Gemini Developer API model list, `generateContent`, `streamGenerateContent`를 지원합니다. 생성은 테스트가 항상 같은 결과를 얻도록 고정된 텍스트 또는 JSON 응답을 반환하고 `fcp-error-rate-limit`, `fcp-error-unavailable` 모델로 429·503 오류를 재현합니다. 호출 기록에는 프로젝트·리전·모델·작업·입력 문자 수·도구 수만 저장하며 프롬프트와 생성 결과 본문은 저장하지 않습니다. PODO backend가 사용하는 Google Gen AI Java SDK 1.58.0을 `GOOGLE_GEMINI_BASE_URL`로 연결해 검증합니다. 실제 모델 의미론, function/tool call 생성, 멀티모달 해석, safety 정책과 토큰 계산 정확도는 미지원입니다.
