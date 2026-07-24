# source examples/demo/env.sh
export GCP_PROJECT_ID=fcp-local
export GOOGLE_CLOUD_PROJECT=fcp-local
export FCP_HTTP_ENDPOINT=http://127.0.0.1:4566
export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export AWS_REGION=ap-northeast-2
export AWS_ENDPOINT_URL=$FCP_HTTP_ENDPOINT
export GCP_METADATA_BASE_URL=$FCP_HTTP_ENDPOINT/computeMetadata/v1
export AUTH_SYSTEM_IDENTITY_JWK_SET_URI=$FCP_HTTP_ENDPOINT/oauth2/v3/certs
export STORAGE_EMULATOR_HOST=http://127.0.0.1:4566
export FCP_GCP_ENDPOINT=127.0.0.1:8085
export PUBSUB_EMULATOR_HOST=127.0.0.1:8085
export FIRESTORE_EMULATOR_HOST=127.0.0.1:8085
export GOOGLE_APPLICATION_CREDENTIALS=${FCP_CREDENTIALS:-$PWD/.fcp/fcp-local-credentials.json}
export GOOGLE_GEMINI_BASE_URL=$FCP_HTTP_ENDPOINT
export GOOGLE_API_KEY=${GOOGLE_API_KEY:-fcp-local}
export GOOGLE_AI_API_KEY=${GOOGLE_AI_API_KEY:-$GOOGLE_API_KEY}
export SPRING_AI_GOOGLE_GENAI_API_KEY=${SPRING_AI_GOOGLE_GENAI_API_KEY:-$GOOGLE_API_KEY}

# docker-compose.yml의 로컬 Cloud SQL / Memorystore 대체 서비스
export CLOUDSQL_HOST=127.0.0.1
export MYSQL_HOST=127.0.0.1
export MYSQL_PORT=3306
export MYSQL_DATABASE=app
export MYSQL_USER=app
export MYSQL_PASSWORD=app
export REDIS_HOST=127.0.0.1
export REDIS_PORT=6379

export PUBSUB_TOPIC_NAME=events
export PUBSUB_SUBSCRIPTION_NAME=events-worker
export GCS_BUCKET=assets
export ASSET_SIGNER_SERVICE_ACCOUNT=fcp-storage-signer@fcp-local.iam.gserviceaccount.com
export JWT_KMS_ACTIVE_KEY_VERSION_NAME=projects/fcp-local/locations/asia-northeast3/keyRings/fcp-local/cryptoKeys/jwt-signing/cryptoKeyVersions/1
