# FCP

FCP(Fake Cloud Platform)는 로컬 개발과 CI 통합 테스트를 위한 경량 클라우드 에뮬레이터입니다. AWS S3/SQS/DynamoDB/STS와 PODO가 사용하는 GCP Storage, Pub/Sub, Firestore, Secret Manager, KMS, IAM Credentials, Compute Metadata 흐름을 공식 SDK·REST 프로토콜로 제공합니다. FCM은 외부 발송 대신 요청을 캡처하고 Vertex AI/Gemini 생성 API는 결정적인 로컬 응답을 반환합니다.

## 빠른 시작

```bash
go run ./cmd/fcp

export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export AWS_REGION=us-east-1
export AWS_ENDPOINT_URL=http://127.0.0.1:4566

aws s3api create-bucket --bucket uploads
aws s3api put-object --bucket uploads --key hello.txt --body ./hello.txt

aws sqs create-queue --queue-name jobs
aws sqs send-message \
  --queue-url http://127.0.0.1:4566/000000000000/jobs \
  --message-body hello

aws dynamodb create-table \
  --table-name local-state \
  --attribute-definitions AttributeName=pk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST

aws sts get-caller-identity
```

데이터는 기본적으로 `.fcp/`에 저장되며 프로세스를 재시작해도 유지됩니다.

```bash
go run ./cmd/fcp --listen 127.0.0.1:4566 --data-dir .fcp
curl http://127.0.0.1:4566/_fcp/health

# 객체·메시지·Firestore 문서·FCM/Vertex 호출 기록만 비우고 리소스 구조와 로컬 키는 유지
curl -X POST http://127.0.0.1:4566/_fcp/actions \
  -H 'Content-Type: application/json' \
  --data '{"operation":"reset-workload"}'

# 모든 리소스, Secret과 로컬 키까지 삭제하는 전체 초기화
curl -X POST http://127.0.0.1:4566/_fcp/reset
```

브라우저에서 `http://127.0.0.1:4566/_fcp/ui`를 열면 서비스별 리소스와 메시지 상태, Vertex AI 생성 호출 메타데이터를 확인하고 S3 버킷, SQS Standard/FIFO 큐, DynamoDB 테이블, Cloud Storage 버킷, Pub/Sub Topic·Subscription을 만들거나 삭제할 수 있습니다. DynamoDB 테이블은 스키마를 유지한 채 아이템만 비울 수 있습니다. 삭제 전에는 확인 창을 표시하며 S3와 Cloud Storage 버킷은 비어 있을 때만 삭제합니다. SQS 큐, Pub/Sub 구독, FCM 캡처와 Vertex AI 호출 기록을 개별로 비우거나 전체 테스트 데이터만 안전하게 초기화할 수도 있습니다. 대시보드는 FCP 바이너리에 내장되며 Secret payload, DynamoDB 아이템, KMS key material, IAM 개인키, 업로드 파트, 메시지 본문, AI 프롬프트와 생성 결과는 표시하지 않습니다.

같은 기능은 로컬 관리 API에서도 사용할 수 있습니다.

```bash
# S3 버킷 생성
curl -X POST http://127.0.0.1:4566/_fcp/actions \
  -H 'Content-Type: application/json' \
  --data '{"operation":"create","service":"s3","kind":"bucket","resource":"local-assets"}'

# 빈 S3 버킷 삭제
curl -X POST http://127.0.0.1:4566/_fcp/actions \
  -H 'Content-Type: application/json' \
  --data '{"operation":"delete","service":"s3","kind":"bucket","resource":"local-assets"}'
```

SDK에서는 endpoint를 `http://127.0.0.1:4566`으로 지정합니다. S3 SDK가 virtual-hosted style을 기본 사용하면 `forcePathStyle` 옵션을 활성화하십시오.

## GCP 연결

Cloud Storage·FCM·Vertex·Compute Metadata와 Secret Manager/KMS REST API는 HTTP 포트, Pub/Sub·Firestore·Secret Manager·KMS·IAM Credentials SDK는 하나의 gRPC 포트를 사용합니다.

```bash
export STORAGE_EMULATOR_HOST=http://127.0.0.1:4566
export PUBSUB_EMULATOR_HOST=127.0.0.1:8085
export FIRESTORE_EMULATOR_HOST=127.0.0.1:8085
export GOOGLE_CLOUD_PROJECT=test-project
export GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:4566
export GOOGLE_API_KEY=fcp-local
```

공식 GCP 클라이언트는 위 환경 변수를 감지해 인증 없이 FCP로 연결됩니다. Google Gen AI SDK는 `GOOGLE_GEMINI_BASE_URL`을 통해 모델 목록과 생성 요청을 FCP로 보냅니다.

```go
storageClient, _ := storage.NewClient(ctx)
_ = storageClient.Bucket("assets").Create(ctx, "test-project", nil)

pubsubClient, _ := pubsub.NewClient(ctx, "test-project")
_, _ = pubsubClient.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{
	Name: "projects/test-project/topics/events",
})
```

Secret Manager, KMS와 IAM Credentials는 표준 emulator 환경 변수가 없으므로 SDK 설정에서 `127.0.0.1:8085`, plaintext 채널, no-credentials를 함께 지정해야 합니다. 언어별 실행 가능한 예제는 [공식 SDK 호환성 테스트](test-clients/README.md)에 있습니다.

PODO의 Node 서버처럼 Metadata Server와 Secret Manager/KMS REST API를 직접 호출하는 애플리케이션은 HTTP 엔드포인트를 사용합니다.

```bash
export FCP_HTTP_ENDPOINT=http://127.0.0.1:4566
export GCP_METADATA_BASE_URL=$FCP_HTTP_ENDPOINT/computeMetadata/v1
export AUTH_SYSTEM_IDENTITY_JWK_SET_URI=$FCP_HTTP_ENDPOINT/oauth2/v3/certs
```

## PODO 프로필

```bash
go run ./cmd/fcp \
  --profile podo \
  --project podo-local \
  --credentials-out .fcp/podo-local-credentials.json
source examples/podo/env.sh
```

프로필은 PODO의 현재 local/dev 명칭에 맞춰 `podo-notification` DynamoDB 테이블, `notification-local`·`reserved-local` SQS 큐, GCS 버킷, Pub/Sub 토픽·구독·DLQ, 빈 JSON Secret과 로컬 DB Secret, KMS 서명/암호화 키, GCS 서명용 IAM 계정을 생성합니다. 큐 구현을 바꿔 확인하려면 `PODO_QUEUE_PROVIDER=sqs` 또는 `pubsub`을 지정한 뒤 `examples/podo/env.sh`를 적용합니다. `--credentials-out` 파일은 같은 로컬 IAM 키를 담기 때문에 Node Storage SDK의 V4 signed URL도 FCP가 실제 검증합니다. 파일 권한은 `0600`으로 기록됩니다. 재시작해도 Secret 버전이나 키가 중복 생성되지 않으며 운영 Secret이나 데이터를 가져오지 않습니다.

MySQL과 Redis까지 함께 띄울 때는 다음 구성을 사용합니다. Cloud SQL과 Memorystore 제어 API를 흉내 내는 대신 애플리케이션이 실제 MySQL/Redis 프로토콜로 연결됩니다.

```bash
docker compose -f docker-compose.podo.yml up --build
```

FCM 발송 결과는 외부로 나가지 않고 조회할 수 있습니다. Vertex AI/Gemini 호출은 프롬프트와 결과 본문을 저장하지 않고 프로젝트·리전·모델·입력 문자 수·도구 수만 기록합니다.

```bash
curl http://127.0.0.1:4566/_fcp/fcm/messages?project=podo-local
curl -X DELETE http://127.0.0.1:4566/_fcp/fcm/messages
curl http://127.0.0.1:4566/_fcp/vertex/generations?project=podo-local
curl -X DELETE http://127.0.0.1:4566/_fcp/vertex/generations
```

## S3 이벤트를 SQS로 전달하기

AWS의 `put-bucket-notification-configuration` API를 그대로 사용합니다.

```bash
QUEUE_URL=$(aws sqs get-queue-url --queue-name jobs --query QueueUrl --output text)
QUEUE_ARN=$(aws sqs get-queue-attributes \
  --queue-url "$QUEUE_URL" \
  --attribute-names QueueArn \
  --query Attributes.QueueArn \
  --output text)

aws s3api put-bucket-notification-configuration \
  --bucket uploads \
  --notification-configuration "{\"QueueConfigurations\":[{\"Id\":\"new-objects\",\"QueueArn\":\"$QUEUE_ARN\",\"Events\":[\"s3:ObjectCreated:*\"]}]}"
```

AWS/GCP 지원 범위와 실제 서비스와의 차이는 [호환성 문서](docs/compatibility.md)에 명시합니다.

## Docker

```bash
docker build -t fcp .
docker run --rm -p 4566:4566 -p 8085:8085 -v fcp-data:/data fcp
```

## 검증

```bash
go test ./...
go vet ./...
```

테스트는 상태 영속성, SQS visibility timeout과 message-attribute MD5, S3 이벤트 필터링, DynamoDB 단건·batch 상태 전환과 GCP 핵심 상태 전환을 검증합니다. 공식 Go SDK 외에 PODO가 사용하는 Java, Kotlin, JavaScript와 AWS SDK 버전도 실제 FCP 프로세스에 연결하는 호환성 테스트를 제공합니다.

> FCP는 요청의 AWS 자격 증명이나 SigV4 서명을 검증하지 않습니다. 기본 바인딩은 loopback이며 신뢰할 수 없는 네트워크에 노출하면 안 됩니다.
