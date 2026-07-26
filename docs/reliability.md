# FCP 신뢰도 점수

FCP의 신뢰도 점수는 코드 커버리지와 별개입니다. 문서화된 API 범위, 실제 클라이언트 증거, 상태 내구성, 실패 격리, 보안과 릴리스 추적성을 합쳐 **로컬 개발·CI 에뮬레이터로서의 신뢰도**를 100점 만점으로 계산합니다.

- 모델: `fcp.reliability/v1`
- 현재 점수: **92/100 · 높음**
- 권장 하한: **85점**
- 증거 방식: `versioned-repository-controls`

> [!IMPORTANT]
> 이 점수는 버전 관리된 테스트·문서·CI 게이트의 설계 점수입니다. 최근 CI 실행이 성공했음을 증명하지 않으며 AWS·Google Cloud 전체 동등성 점수도 아닙니다.

## 점수 구성

| 영역 | 점수 | 상태 | 평가 내용 |
|---|---:|---|---|
| API 충실도 | 24/30 | PARTIAL | 문서화된 작업의 FULL·PARTIAL 범위를 가중 합산합니다. |
| 클라이언트 현실성 | 20/20 | FULL | 실제 SDK·wire contract 증거와 외부 클라이언트 실행 경로 계측을 평가합니다. |
| 상태 내구성 | 20/20 | FULL | 저장 실패, 객체 무결성, 재시작과 단일 writer 안전성을 평가합니다. |
| 실패 격리·동시성 | 14/15 | PARTIAL | 부분 적용, crash 경계, race와 오류 응답의 검증 폭을 평가합니다. |
| 보안·공급망 | 9/10 | PARTIAL | 취약점, 비밀정보 노출 방지와 의존성 재현성을 평가합니다. |
| 릴리스 추적성 | 5/5 | FULL | 검증 소스에서 산출물까지의 재현성과 증명을 평가합니다. |
| **합계** | **92/100** | **높음** | 권장 하한 85점 |

## Provider별 점수

| Provider | 점수 | 등급 | API 충실도 | 클라이언트 현실성 |
|---|---:|---|---:|---:|
| AWS | 88/100 | 높음 | 20/30 | 20/20 |
| GCP | 94/100 | 높음 | 27/30 | 19/20 |

Provider 점수는 해당 Provider의 API·클라이언트 증거만 다시 계산하고, 공통 상태 엔진·보안·릴리스 기준은 동일하게 적용합니다.

## 등급 기준

| 점수 | 등급 | 사용 판단 |
|---:|---|---|
| 95–100 | 매우 높음 | 정의된 로컬 범위에서 폭넓고 반복 가능한 증거가 있음 |
| 85–94 | 높음 | 일반적인 로컬 통합 테스트와 CI 차단 기준으로 권장 |
| 70–84 | 보통 | 핵심 경로에는 쓸 수 있지만 중요한 부분 증거가 부족함 |
| 50–69 | 제한적 | 보조 개발 도구로만 사용하고 실제 클라우드 검증이 필요함 |
| 0–49 | 낮음 | 회귀 판단 근거로 사용하기 어려움 |

가중 평균이 치명적인 결함을 숨기지 못하도록 다음 hard cap을 적용합니다.

- API 충실도 또는 클라이언트 증거가 0점이면 최대 49점
- 상태 내구성이 15/20 미만이면 최대 69점
- 실패 격리·동시성이 9/15 미만이면 최대 69점
- 보안·공급망이 6/10 미만이면 최대 69점

## 세부 기준과 증거

### API 충실도 · 24/30

| 기준 | 점수 | 상태 | 계산·판단 근거 |
|---|---:|---|---|
| 작업별 호환 범위 | 24/30 | PARTIAL | FULL 39개는 100%, PARTIAL 23개는 50%로 계산합니다. 증거: `docs/compatibility.md` |

### 클라이언트 현실성 · 20/20

| 기준 | 점수 | 상태 | 계산·판단 근거 |
|---|---:|---|---|
| 공식 클라이언트·계약 증거 | 15/15 | FULL | SDK 검증 11개는 100%, HTTP 계약 검증 2개는 80%로 계산합니다. 증거: `docs/compatibility.md`, `test-clients/README.md` |
| 공식 SDK 서버 경로 계측 | 5/5 | FULL | CI가 공식 SDK를 매번 재실행하고 실제 서버 패키지 경로 커버리지 40%를 강제합니다. 증거: `.github/workflows/ci.yml`, `scripts/check-coverage.sh` |

### 상태 내구성 · 20/20

| 기준 | 점수 | 상태 | 계산·판단 근거 |
|---|---:|---|---|
| 원자적 커밋과 전체 롤백 | 5/5 | FULL | state.json 커밋 전 실패는 마지막 커밋 상태로 전체 롤백합니다. 증거: `internal/state/integrity.go`, `internal/state/store_test.go` |
| 객체 세대와 SHA-256 무결성 | 5/5 | FULL | 객체 본문을 내용 세대로 분리하고 startup·strict 검사를 제공합니다. 증거: `internal/state/integrity.go`, `internal/state/store_test.go` |
| 스냅샷 crash recovery | 4/4 | FULL | restore journal의 모든 교체 지점과 동일 상태 복구를 검증합니다. 증거: `internal/state/snapshot.go`, `internal/state/snapshot_test.go` |
| 단일 writer와 fsync | 3/3 | FULL | 동일 데이터 디렉터리의 두 번째 writer를 거절하고 디렉터리를 동기화합니다. 증거: `internal/state/lock_test.go`, `internal/state/durability_unix.go` |
| 재시작 영속성 | 3/3 | FULL | 주요 AWS·GCP 상태를 닫고 다시 연 뒤 결과를 검증합니다. 증거: `internal/state/store_test.go`, `internal/state/gcp_test.go`, `internal/state/dynamodb_test.go` |

### 실패 격리·동시성 · 14/15

| 기준 | 점수 | 상태 | 계산·판단 근거 |
|---|---:|---|---|
| 저장 실패 주입 | 4/4 | FULL | AWS·GCP 상태 변경이 실패 뒤 메모리와 다음 저장에 남지 않는지 검증합니다. 증거: `internal/state/store_test.go` |
| 다중 작업 원자성 | 3/3 | FULL | DynamoDB transaction과 Firestore mutation이 중간 오류 때 부분 적용되지 않습니다. 증거: `internal/state/dynamodb_test.go`, `internal/state/gcp_services_test.go` |
| 프로세스 중단 경계 | 3/3 | FULL | 스냅샷 복원의 journal-written부터 state-committed까지 복구합니다. 증거: `internal/state/snapshot_test.go` |
| race detector와 writer lock | 3/3 | FULL | 전체 Go 패키지 race 검사와 data-dir lock 테스트를 CI에서 강제합니다. 증거: `.github/workflows/ci.yml`, `internal/state/lock_test.go` |
| 오류·부분 실패 계약 | 1/2 | PARTIAL | 주요 오류는 검증하지만 모든 PARTIAL 작업의 오류 조합을 exhaustive하게 검증하지는 않습니다. 증거: `internal/server/server_test.go`, `internal/server/gcp_test.go` |

### 보안·공급망 · 9/10

| 기준 | 점수 | 상태 | 계산·판단 근거 |
|---|---:|---|---|
| Go·컨테이너 취약점 차단 | 2/2 | FULL | 알려진 Go 취약점과 수정 가능한 HIGH·CRITICAL 이미지 취약점을 차단합니다. 증거: `.github/workflows/ci.yml` |
| 클라이언트 의존성 audit | 1/2 | PARTIAL | 정확히 고정된 test-only moderate 예외 1건이 있어 부분 점수입니다. 증거: `test-clients/javascript/audit-policy.json`, `scripts/verify-pnpm-audit.mjs` |
| 민감 데이터 비노출 | 2/2 | FULL | 대시보드와 CLI가 Secret·key·message·prompt payload를 표시하지 않습니다. 증거: `internal/server/dashboard_test.go`, `internal/cli/cli_test.go` |
| 불변 빌드 입력 | 2/2 | FULL | Actions·base image를 digest로 고정하고 JVM·JavaScript lock을 검증합니다. 증거: `.github/workflows/ci.yml`, `Dockerfile`, `test-clients/jvm/backend/gradle.lockfile` |
| 로컬 전용 경계 | 2/2 | FULL | 기본 loopback과 명시된 인증 제외 범위로 오용을 제한합니다. 증거: `cmd/fcp/main.go`, `README.md` |

### 릴리스 추적성 · 5/5

| 기준 | 점수 | 상태 | 계산·판단 근거 |
|---|---:|---|---|
| 바이너리와 checksum | 1/1 | FULL | 지원 플랫폼 바이너리와 SHA-256 checksum을 함께 생성합니다. 증거: `scripts/build-release.sh` |
| 멀티 아키텍처 SBOM·provenance | 2/2 | FULL | amd64·arm64 이미지에 SBOM과 max provenance를 생성합니다. 증거: `.github/workflows/release.yml` |
| 산출물 attestation 검증 | 1/1 | FULL | 게시 전에 바이너리와 이미지 attestation을 다시 검증합니다. 증거: `.github/workflows/release.yml` |
| 릴리스 전체 CI 재사용 | 1/1 | FULL | Release가 동일한 전체 CI workflow 성공 뒤에만 산출물을 만듭니다. 증거: `.github/workflows/release.yml` |

## 실행과 판정

```bash
fcp reliability --json
fcp reliability --minimum 90 --json
make verify
```

`fcp reliability`는 실행 중인 FCP가 제공하는 버전 관리된 평가표를 출력하고 지정한 최소 점수보다 낮으면 exit code `1`을 반환합니다. `make verify`와 CI는 평가표의 증거가 실제 테스트를 통과하는지 별도로 검사합니다.

## 새 기능의 합격 기준

1. 호환성 카탈로그에 지원 동작과 제외 범위를 명시합니다.
2. 공식 SDK 또는 실제 wire-format 요청으로 성공 경로를 검증합니다.
3. 입력 오류, not-found, 조건 실패와 부분 실패를 계약 테스트로 고정합니다.
4. 상태 변경이면 저장 실패 롤백과 재시작 영속성 테스트를 추가합니다.
5. 전체 Go·공식 SDK 서버 경로 커버리지 하한을 유지하거나 올립니다.
6. 점수가 낮아지면 변경된 기준과 위험을 명시하며, hard cap을 우회하지 않습니다.

## 의도적으로 보장하지 않는 범위

- 점수는 FCP가 문서화한 로컬 개발·CI 범위만 평가하며 실제 AWS·Google Cloud 전체 동등성을 뜻하지 않습니다.
- 런타임에 표시되는 값은 버전 관리된 테스트와 CI 게이트의 설계 점수이며 최근 CI 실행 시각이나 성공 여부를 증명하지 않습니다.
- IAM 정책, 실제 자격 증명·SigV4, quota, 성능, 리전 장애와 분산 일관성은 실제 클라우드에서 별도 검증해야 합니다.

정확한 서비스별 지원 범위는 [호환성 문서](compatibility.md)가 기준입니다.
