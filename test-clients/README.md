# 공식 SDK 호환성 테스트

이 디렉터리는 FCP 전용 SDK를 만들지 않고 PODO가 사용하는 공식 SDK를 그대로 FCP에 연결하는 회귀 테스트다.

- JVM: PODO backend의 Storage 2.68.0, Pub/Sub 1.140.1, KMS 2.96.0, Google Gen AI 1.58.0과 notification의 Spring Cloud GCP BOM 7.4.6/Secret Manager 2.59.0, AWS DynamoDB·SQS·STS 2.33.9를 사용한다. notification의 FCP 전용 FCM HTTP 경로도 같은 요청 형식으로 검증한다.
- Kotlin: 같은 JVM 공식 SDK로 Firestore와 Secret Manager를 검증한다.
- JavaScript: AWS SDK v3.1092.0의 S3/SQS, `lib-storage` 멀티파트 업로드와 SQS DLQ redrive/FIFO ordering/deduplication, podo-app lockfile의 Storage 7.19.0/Pub/Sub 4.11.0을 사용하고, Metadata Server·Secret Manager REST·KMS REST 호출을 검증한다.

FCP를 먼저 실행한 뒤 테스트한다.

```bash
go run ./cmd/fcp \
  --profile podo \
  --project podo-local \
  --credentials-out .fcp/podo-local-credentials.json

FCP_HTTP_ENDPOINT=http://127.0.0.1:4566 \
FCP_GCP_ENDPOINT=127.0.0.1:8085 \
AWS_ACCESS_KEY_ID=test \
AWS_SECRET_ACCESS_KEY=test \
AWS_REGION=ap-northeast-2 \
AWS_ENDPOINT_URL=http://127.0.0.1:4566 \
GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:4566 \
../podo-notification/gradlew -p test-clients/jvm test

cd test-clients/javascript
pnpm install
FCP_HTTP_ENDPOINT=http://127.0.0.1:4566 \
STORAGE_EMULATOR_HOST=http://127.0.0.1:4566 \
PUBSUB_EMULATOR_HOST=127.0.0.1:8085 \
AWS_ENDPOINT_URL=http://127.0.0.1:4566 \
GOOGLE_APPLICATION_CREDENTIALS=$PWD/../../.fcp/podo-local-credentials.json \
pnpm test
```
