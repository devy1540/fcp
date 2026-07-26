package reliability

import (
	"fmt"
	"math"
	"slices"

	"github.com/devy1540/fcp/internal/compatibility"
)

const (
	ModelVersion       = "fcp.reliability/v1"
	Source             = "docs/reliability.md"
	RecommendedMinimum = 85
)

type Assessment struct {
	ModelVersion       string               `json:"modelVersion"`
	Scope              string               `json:"scope"`
	Score              int                  `json:"score"`
	Rating             string               `json:"rating"`
	RatingLabel        string               `json:"ratingLabel"`
	RecommendedMinimum int                  `json:"recommendedMinimum"`
	Passed             bool                 `json:"passed"`
	EvidenceBasis      string               `json:"evidenceBasis"`
	Dimensions         []Dimension          `json:"dimensions"`
	Providers          []ProviderAssessment `json:"providers"`
	AppliedCaps        []ScoreCap           `json:"appliedCaps,omitempty"`
	Limitations        []string             `json:"limitations"`
}

type Dimension struct {
	ID          string      `json:"id"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	Earned      int         `json:"earned"`
	Weight      int         `json:"weight"`
	Status      string      `json:"status"`
	Critical    bool        `json:"critical"`
	Criteria    []Criterion `json:"criteria"`
}

type Criterion struct {
	ID       string        `json:"id"`
	Label    string        `json:"label"`
	Earned   int           `json:"earned"`
	Weight   int           `json:"weight"`
	Status   string        `json:"status"`
	Detail   string        `json:"detail"`
	Evidence []EvidenceRef `json:"evidence"`
}

type EvidenceRef struct {
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

type ProviderAssessment struct {
	Provider             string `json:"provider"`
	Score                int    `json:"score"`
	Rating               string `json:"rating"`
	RatingLabel          string `json:"ratingLabel"`
	APIFidelityScore     int    `json:"apiFidelityScore"`
	ClientRealismScore   int    `json:"clientRealismScore"`
	RecommendedMinimum   int    `json:"recommendedMinimum"`
	PassesRecommendation bool   `json:"passesRecommendation"`
}

type ScoreCap struct {
	Dimension string `json:"dimension"`
	Maximum   int    `json:"maximum"`
	Reason    string `json:"reason"`
}

type RatingBand struct {
	Minimum int
	Rating  string
	Label   string
	Meaning string
}

func RatingBands() []RatingBand {
	return []RatingBand{
		{Minimum: 95, Rating: "VERY_HIGH", Label: "매우 높음", Meaning: "정의된 로컬 범위에서 폭넓고 반복 가능한 증거가 있음"},
		{Minimum: 85, Rating: "HIGH", Label: "높음", Meaning: "일반적인 로컬 통합 테스트와 CI 차단 기준으로 권장"},
		{Minimum: 70, Rating: "MODERATE", Label: "보통", Meaning: "핵심 경로에는 쓸 수 있지만 중요한 부분 증거가 부족함"},
		{Minimum: 50, Rating: "LIMITED", Label: "제한적", Meaning: "보조 개발 도구로만 사용하고 실제 클라우드 검증이 필요함"},
		{Minimum: 0, Rating: "LOW", Label: "낮음", Meaning: "회귀 판단 근거로 사용하기 어려움"},
	}
}

func Assess(services []compatibility.Service) Assessment {
	dimensions := assessmentDimensions(services)
	score, caps := scoreDimensions(dimensions)
	rating, label := ratingFor(score)
	providers := make([]ProviderAssessment, 0, 2)
	for _, provider := range []string{"AWS", "GCP"} {
		selected := make([]compatibility.Service, 0)
		for _, service := range services {
			if service.Provider == provider {
				selected = append(selected, service)
			}
		}
		providerDimensions := assessmentDimensions(selected)
		providerScore, providerCaps := scoreDimensions(providerDimensions)
		providerRating, providerLabel := ratingFor(providerScore)
		providers = append(providers, ProviderAssessment{
			Provider: provider, Score: providerScore, Rating: providerRating, RatingLabel: providerLabel,
			APIFidelityScore:   dimensionByID(providerDimensions, "api_fidelity").Earned,
			ClientRealismScore: dimensionByID(providerDimensions, "client_realism").Earned,
			RecommendedMinimum: RecommendedMinimum, PassesRecommendation: providerScore >= RecommendedMinimum && len(providerCaps) == 0,
		})
	}
	return Assessment{
		ModelVersion:       ModelVersion,
		Scope:              "local-development-and-ci",
		Score:              score,
		Rating:             rating,
		RatingLabel:        label,
		RecommendedMinimum: RecommendedMinimum,
		Passed:             score >= RecommendedMinimum && len(caps) == 0,
		EvidenceBasis:      "versioned-repository-controls",
		Dimensions:         dimensions,
		Providers:          providers,
		AppliedCaps:        caps,
		Limitations: []string{
			"점수는 FCP가 문서화한 로컬 개발·CI 범위만 평가하며 실제 AWS·Google Cloud 전체 동등성을 뜻하지 않습니다.",
			"런타임에 표시되는 값은 버전 관리된 테스트와 CI 게이트의 설계 점수이며 최근 CI 실행 시각이나 성공 여부를 증명하지 않습니다.",
			"IAM 정책, 실제 자격 증명·SigV4, quota, 성능, 리전 장애와 분산 일관성은 실제 클라우드에서 별도 검증해야 합니다.",
		},
	}
}

func Validate(assessment Assessment) error {
	if assessment.ModelVersion != ModelVersion {
		return fmt.Errorf("unexpected reliability model %q", assessment.ModelVersion)
	}
	if assessment.Scope != "local-development-and-ci" || assessment.EvidenceBasis != "versioned-repository-controls" {
		return fmt.Errorf("unexpected reliability scope or evidence basis")
	}
	expectedDimensions := map[string]struct {
		weight   int
		critical bool
	}{
		"api_fidelity":          {weight: 30, critical: true},
		"client_realism":        {weight: 20, critical: true},
		"state_safety":          {weight: 20, critical: true},
		"failure_isolation":     {weight: 15, critical: true},
		"security_supply_chain": {weight: 10, critical: true},
		"release_traceability":  {weight: 5, critical: false},
	}
	totalWeight := 0
	seen := map[string]bool{}
	for _, dimension := range assessment.Dimensions {
		if dimension.ID == "" || dimension.Label == "" || dimension.Description == "" || seen[dimension.ID] {
			return fmt.Errorf("invalid reliability dimension %q", dimension.ID)
		}
		expected, exists := expectedDimensions[dimension.ID]
		if !exists || dimension.Weight != expected.weight || dimension.Critical != expected.critical {
			return fmt.Errorf("dimension %s does not match model %s", dimension.ID, ModelVersion)
		}
		seen[dimension.ID] = true
		if dimension.Weight <= 0 || dimension.Earned < 0 || dimension.Earned > dimension.Weight {
			return fmt.Errorf("invalid reliability points for %s", dimension.ID)
		}
		if dimension.Status != pointStatus(dimension.Earned, dimension.Weight) {
			return fmt.Errorf("dimension %s has inconsistent status %q", dimension.ID, dimension.Status)
		}
		criterionWeight := 0
		criterionEarned := 0
		criterionIDs := map[string]bool{}
		for _, criterion := range dimension.Criteria {
			if criterion.ID == "" || criterion.Label == "" || criterion.Detail == "" || len(criterion.Evidence) == 0 || criterionIDs[criterion.ID] {
				return fmt.Errorf("dimension %s has incomplete criterion %q", dimension.ID, criterion.ID)
			}
			criterionIDs[criterion.ID] = true
			if criterion.Weight <= 0 || criterion.Earned < 0 || criterion.Earned > criterion.Weight {
				return fmt.Errorf("criterion %s has invalid points", criterion.ID)
			}
			if criterion.Status != pointStatus(criterion.Earned, criterion.Weight) {
				return fmt.Errorf("criterion %s has inconsistent status %q", criterion.ID, criterion.Status)
			}
			for _, evidence := range criterion.Evidence {
				if evidence.Path == "" || evidence.Detail == "" {
					return fmt.Errorf("criterion %s has incomplete evidence", criterion.ID)
				}
			}
			criterionWeight += criterion.Weight
			criterionEarned += criterion.Earned
		}
		if criterionWeight != dimension.Weight || criterionEarned != dimension.Earned {
			return fmt.Errorf("dimension %s points do not match its criteria", dimension.ID)
		}
		totalWeight += dimension.Weight
	}
	if totalWeight != 100 || len(seen) != len(expectedDimensions) {
		return fmt.Errorf("reliability weights total %d, want 100", totalWeight)
	}
	if assessment.Score < 0 || assessment.Score > 100 {
		return fmt.Errorf("reliability score %d is outside 0..100", assessment.Score)
	}
	expectedScore, expectedCaps := scoreDimensions(assessment.Dimensions)
	if assessment.Score != expectedScore || !slices.Equal(assessment.AppliedCaps, expectedCaps) {
		return fmt.Errorf("reliability score or caps do not match the model")
	}
	rating, label := ratingFor(assessment.Score)
	if assessment.Rating != rating || assessment.RatingLabel != label {
		return fmt.Errorf("reliability rating does not match score %d", assessment.Score)
	}
	if assessment.RecommendedMinimum != RecommendedMinimum ||
		assessment.Passed != (assessment.Score >= RecommendedMinimum && len(assessment.AppliedCaps) == 0) {
		return fmt.Errorf("reliability recommendation does not match score %d", assessment.Score)
	}
	if len(assessment.Limitations) == 0 {
		return fmt.Errorf("reliability limitations are missing")
	}
	providers := map[string]bool{}
	for _, provider := range assessment.Providers {
		if (provider.Provider != "AWS" && provider.Provider != "GCP") || providers[provider.Provider] {
			return fmt.Errorf("invalid reliability provider %q", provider.Provider)
		}
		providers[provider.Provider] = true
		if provider.Score < 0 || provider.Score > 100 ||
			provider.APIFidelityScore < 0 || provider.APIFidelityScore > 30 ||
			provider.ClientRealismScore < 0 || provider.ClientRealismScore > 20 {
			return fmt.Errorf("invalid reliability points for provider %s", provider.Provider)
		}
		providerRating, providerLabel := ratingFor(provider.Score)
		if provider.Rating != providerRating || provider.RatingLabel != providerLabel ||
			provider.RecommendedMinimum != RecommendedMinimum ||
			(provider.PassesRecommendation && provider.Score < RecommendedMinimum) {
			return fmt.Errorf("invalid reliability recommendation for provider %s", provider.Provider)
		}
	}
	if len(providers) != 2 {
		return fmt.Errorf("reliability provider assessments are incomplete")
	}
	return nil
}

func assessmentDimensions(services []compatibility.Service) []Dimension {
	return []Dimension{
		apiFidelityDimension(services),
		clientRealismDimension(services),
		stateSafetyDimension(),
		failureIsolationDimension(),
		securityDimension(),
		releaseDimension(),
	}
}

func apiFidelityDimension(services []compatibility.Service) Dimension {
	full, partial := 0, 0
	for _, service := range services {
		for _, operation := range service.Operations {
			switch operation.Status {
			case "FULL":
				full++
			case "PARTIAL":
				partial++
			}
		}
	}
	operationCount := full + partial
	earned := 0
	if operationCount > 0 {
		earned = int(math.Round(30 * (float64(full) + 0.5*float64(partial)) / float64(operationCount)))
	}
	return dimension(
		"api_fidelity", "API 충실도", "문서화된 작업의 FULL·PARTIAL 범위를 가중 합산합니다.", earned, 30, true,
		criterion(
			"operation_scope", "작업별 호환 범위", earned, 30,
			fmt.Sprintf("FULL %d개는 100%%, PARTIAL %d개는 50%%로 계산합니다.", full, partial),
			evidence(compatibility.Source, "서비스별 작업 상태의 단일 기준"),
		),
	)
}

func clientRealismDimension(services []compatibility.Service) Dimension {
	sdk, contract := 0, 0
	for _, service := range services {
		switch service.Level {
		case "SDK":
			sdk++
		case "CONTRACT":
			contract++
		}
	}
	serviceCount := sdk + contract
	evidenceEarned := 0
	executionEarned := 0
	if serviceCount > 0 {
		evidenceEarned = int(math.Round(15 * (float64(sdk) + 0.8*float64(contract)) / float64(serviceCount)))
		executionEarned = 5
	}
	return dimension(
		"client_realism", "클라이언트 현실성", "실제 SDK·wire contract 증거와 외부 클라이언트 실행 경로 계측을 평가합니다.",
		evidenceEarned+executionEarned, 20, true,
		criterion(
			"client_evidence", "공식 클라이언트·계약 증거", evidenceEarned, 15,
			fmt.Sprintf("SDK 검증 %d개는 100%%, HTTP 계약 검증 %d개는 80%%로 계산합니다.", sdk, contract),
			evidence(compatibility.Source, "서비스별 SDK와 HTTP 계약 버전"),
			evidence("test-clients/README.md", "실행 가능한 공식 클라이언트 목록"),
		),
		criterion(
			"executed_server_paths", "공식 SDK 서버 경로 계측", executionEarned, 5,
			"CI가 공식 SDK를 매번 재실행하고 실제 서버 패키지 경로 커버리지 40%를 강제합니다.",
			evidence(".github/workflows/ci.yml", "coverage-instrumented FCP와 --rerun-tasks"),
			evidence("scripts/check-coverage.sh", "수치 하한 검증"),
		),
	)
}

func stateSafetyDimension() Dimension {
	return dimension(
		"state_safety", "상태 내구성", "저장 실패, 객체 무결성, 재시작과 단일 writer 안전성을 평가합니다.", 20, 20, true,
		criterion("atomic_commit", "원자적 커밋과 전체 롤백", 5, 5, "state.json 커밋 전 실패는 마지막 커밋 상태로 전체 롤백합니다.",
			evidence("internal/state/integrity.go", "committed snapshot rollback"),
			evidence("internal/state/store_test.go", "서비스별 저장 실패 주입과 재시작 검증")),
		criterion("object_integrity", "객체 세대와 SHA-256 무결성", 5, 5, "객체 본문을 내용 세대로 분리하고 startup·strict 검사를 제공합니다.",
			evidence("internal/state/integrity.go", "객체 크기와 digest 검사"),
			evidence("internal/state/store_test.go", "누락·변조·고아 파일 검증")),
		criterion("snapshot_recovery", "스냅샷 crash recovery", 4, 4, "restore journal의 모든 교체 지점과 동일 상태 복구를 검증합니다.",
			evidence("internal/state/snapshot.go", "restore journal recovery"),
			evidence("internal/state/snapshot_test.go", "crash-point matrix")),
		criterion("single_writer", "단일 writer와 fsync", 3, 3, "동일 데이터 디렉터리의 두 번째 writer를 거절하고 디렉터리를 동기화합니다.",
			evidence("internal/state/lock_test.go", "process lock 검증"),
			evidence("internal/state/durability_unix.go", "파일시스템 durability 구현")),
		criterion("reopen_persistence", "재시작 영속성", 3, 3, "주요 AWS·GCP 상태를 닫고 다시 연 뒤 결과를 검증합니다.",
			evidence("internal/state/store_test.go", "S3·SQS 재시작 검증"),
			evidence("internal/state/gcp_test.go", "GCS·Pub/Sub 재시작 검증"),
			evidence("internal/state/dynamodb_test.go", "DynamoDB 재시작 검증")),
	)
}

func failureIsolationDimension() Dimension {
	return dimension(
		"failure_isolation", "실패 격리·동시성", "부분 적용, crash 경계, race와 오류 응답의 검증 폭을 평가합니다.", 14, 15, true,
		criterion("save_fault_injection", "저장 실패 주입", 4, 4, "AWS·GCP 상태 변경이 실패 뒤 메모리와 다음 저장에 남지 않는지 검증합니다.",
			evidence("internal/state/store_test.go", "save failure rollback matrix")),
		criterion("multi_operation_atomicity", "다중 작업 원자성", 3, 3, "DynamoDB transaction과 Firestore mutation이 중간 오류 때 부분 적용되지 않습니다.",
			evidence("internal/state/dynamodb_test.go", "transaction atomicity"),
			evidence("internal/state/gcp_services_test.go", "Firestore mutation isolation")),
		criterion("crash_boundaries", "프로세스 중단 경계", 3, 3, "스냅샷 복원의 journal-written부터 state-committed까지 복구합니다.",
			evidence("internal/state/snapshot_test.go", "restore crash phases")),
		criterion("race_and_lock", "race detector와 writer lock", 3, 3, "전체 Go 패키지 race 검사와 data-dir lock 테스트를 CI에서 강제합니다.",
			evidence(".github/workflows/ci.yml", "go test -race"),
			evidence("internal/state/lock_test.go", "writer exclusion")),
		criterion("negative_paths", "오류·부분 실패 계약", 1, 2, "주요 오류는 검증하지만 모든 PARTIAL 작업의 오류 조합을 exhaustive하게 검증하지는 않습니다.",
			evidence("internal/server/server_test.go", "AWS 오류와 batch 부분 실패"),
			evidence("internal/server/gcp_test.go", "GCP gRPC lifecycle과 오류")),
	)
}

func securityDimension() Dimension {
	return dimension(
		"security_supply_chain", "보안·공급망", "취약점, 비밀정보 노출 방지와 의존성 재현성을 평가합니다.", 9, 10, true,
		criterion("vulnerability_gates", "Go·컨테이너 취약점 차단", 2, 2, "알려진 Go 취약점과 수정 가능한 HIGH·CRITICAL 이미지 취약점을 차단합니다.",
			evidence(".github/workflows/ci.yml", "govulncheck와 Trivy gate")),
		criterion("dependency_audit", "클라이언트 의존성 audit", 1, 2, "정확히 고정된 test-only moderate 예외 1건이 있어 부분 점수입니다.",
			evidence("test-clients/javascript/audit-policy.json", "패키지·경로·버전·만료일 제한"),
			evidence("scripts/verify-pnpm-audit.mjs", "예외 드리프트와 HIGH·CRITICAL 거절")),
		criterion("sensitive_redaction", "민감 데이터 비노출", 2, 2, "대시보드와 CLI가 Secret·key·message·prompt payload를 표시하지 않습니다.",
			evidence("internal/server/dashboard_test.go", "dashboard sensitive-field contract"),
			evidence("internal/cli/cli_test.go", "metadata-only CLI contract")),
		criterion("immutable_dependencies", "불변 빌드 입력", 2, 2, "Actions·base image를 digest로 고정하고 JVM·JavaScript lock을 검증합니다.",
			evidence(".github/workflows/ci.yml", "immutable action references"),
			evidence("Dockerfile", "digest-pinned base images"),
			evidence("test-clients/jvm/backend/gradle.lockfile", "JVM dependency lock")),
		criterion("local_boundary", "로컬 전용 경계", 2, 2, "기본 loopback과 명시된 인증 제외 범위로 오용을 제한합니다.",
			evidence("cmd/fcp/main.go", "loopback default listeners"),
			evidence("README.md", "production exposure warning")),
	)
}

func releaseDimension() Dimension {
	return dimension(
		"release_traceability", "릴리스 추적성", "검증 소스에서 산출물까지의 재현성과 증명을 평가합니다.", 5, 5, false,
		criterion("checksums", "바이너리와 checksum", 1, 1, "지원 플랫폼 바이너리와 SHA-256 checksum을 함께 생성합니다.",
			evidence("scripts/build-release.sh", "deterministic release archives")),
		criterion("image_metadata", "멀티 아키텍처 SBOM·provenance", 2, 2, "amd64·arm64 이미지에 SBOM과 max provenance를 생성합니다.",
			evidence(".github/workflows/release.yml", "buildx multi-platform metadata")),
		criterion("attestations", "산출물 attestation 검증", 1, 1, "게시 전에 바이너리와 이미지 attestation을 다시 검증합니다.",
			evidence(".github/workflows/release.yml", "GitHub attestation verify")),
		criterion("release_quality", "릴리스 전체 CI 재사용", 1, 1, "Release가 동일한 전체 CI workflow 성공 뒤에만 산출물을 만듭니다.",
			evidence(".github/workflows/release.yml", "reusable CI quality job")),
	)
}

func scoreDimensions(dimensions []Dimension) (int, []ScoreCap) {
	score := 0
	for _, dimension := range dimensions {
		score += dimension.Earned
	}
	caps := []ScoreCap{}
	for _, rule := range []struct {
		id      string
		minimum int
		maximum int
		reason  string
	}{
		{id: "api_fidelity", minimum: 1, maximum: 49, reason: "API 호환 범위 증거가 없습니다."},
		{id: "client_realism", minimum: 1, maximum: 49, reason: "실제 클라이언트 또는 wire contract 증거가 없습니다."},
		{id: "state_safety", minimum: 15, maximum: 69, reason: "상태 내구성 핵심 기준이 75% 미만입니다."},
		{id: "failure_isolation", minimum: 9, maximum: 69, reason: "실패 격리 핵심 기준이 60% 미만입니다."},
		{id: "security_supply_chain", minimum: 6, maximum: 69, reason: "보안·공급망 핵심 기준이 60% 미만입니다."},
	} {
		dimension := dimensionByID(dimensions, rule.id)
		if dimension.Earned >= rule.minimum {
			continue
		}
		caps = append(caps, ScoreCap{Dimension: rule.id, Maximum: rule.maximum, Reason: rule.reason})
		score = min(score, rule.maximum)
	}
	return score, caps
}

func dimension(id, label, description string, earned, weight int, critical bool, criteria ...Criterion) Dimension {
	return Dimension{
		ID: id, Label: label, Description: description, Earned: earned, Weight: weight,
		Status: pointStatus(earned, weight), Critical: critical, Criteria: criteria,
	}
}

func criterion(id, label string, earned, weight int, detail string, evidenceRefs ...EvidenceRef) Criterion {
	return Criterion{
		ID: id, Label: label, Earned: earned, Weight: weight,
		Status: pointStatus(earned, weight), Detail: detail, Evidence: evidenceRefs,
	}
}

func evidence(path, detail string) EvidenceRef {
	return EvidenceRef{Path: path, Detail: detail}
}

func pointStatus(earned, weight int) string {
	switch {
	case earned == weight:
		return "FULL"
	case earned > 0:
		return "PARTIAL"
	default:
		return "MISSING"
	}
}

func ratingFor(score int) (string, string) {
	for _, band := range RatingBands() {
		if score >= band.Minimum {
			return band.Rating, band.Label
		}
	}
	return "LOW", "낮음"
}

func dimensionByID(dimensions []Dimension, id string) Dimension {
	index := slices.IndexFunc(dimensions, func(dimension Dimension) bool { return dimension.ID == id })
	if index < 0 {
		return Dimension{}
	}
	return dimensions[index]
}
