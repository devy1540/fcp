package reliability

import (
	"fmt"
	"strings"

	"github.com/devy1540/fcp/internal/compatibility"
)

func Markdown() string {
	assessment := Assess(compatibility.Services())
	var document strings.Builder
	document.WriteString("# FCP 신뢰도 점수\n\n")
	document.WriteString("FCP의 신뢰도 점수는 코드 커버리지와 별개입니다. 문서화된 API 범위, 실제 클라이언트 증거, 상태 내구성, 실패 격리, 보안과 릴리스 추적성을 합쳐 **로컬 개발·CI 에뮬레이터로서의 신뢰도**를 100점 만점으로 계산합니다.\n\n")
	fmt.Fprintf(&document, "- 모델: `%s`\n", assessment.ModelVersion)
	fmt.Fprintf(&document, "- 현재 점수: **%d/100 · %s**\n", assessment.Score, assessment.RatingLabel)
	fmt.Fprintf(&document, "- 권장 하한: **%d점**\n", assessment.RecommendedMinimum)
	fmt.Fprintf(&document, "- 증거 방식: `%s`\n\n", assessment.EvidenceBasis)
	document.WriteString("> [!IMPORTANT]\n")
	document.WriteString("> 이 점수는 버전 관리된 테스트·문서·CI 게이트의 설계 점수입니다. 최근 CI 실행이 성공했음을 증명하지 않으며 AWS·Google Cloud 전체 동등성 점수도 아닙니다.\n\n")

	document.WriteString("## 점수 구성\n\n")
	document.WriteString("| 영역 | 점수 | 상태 | 평가 내용 |\n")
	document.WriteString("|---|---:|---|---|\n")
	for _, dimension := range assessment.Dimensions {
		fmt.Fprintf(&document, "| %s | %d/%d | %s | %s |\n", dimension.Label, dimension.Earned, dimension.Weight, dimension.Status, dimension.Description)
	}
	fmt.Fprintf(&document, "| **합계** | **%d/100** | **%s** | 권장 하한 %d점 |\n\n", assessment.Score, assessment.RatingLabel, assessment.RecommendedMinimum)

	document.WriteString("## Provider별 점수\n\n")
	document.WriteString("| Provider | 점수 | 등급 | API 충실도 | 클라이언트 현실성 |\n")
	document.WriteString("|---|---:|---|---:|---:|\n")
	for _, provider := range assessment.Providers {
		fmt.Fprintf(&document, "| %s | %d/100 | %s | %d/30 | %d/20 |\n", provider.Provider, provider.Score, provider.RatingLabel, provider.APIFidelityScore, provider.ClientRealismScore)
	}
	document.WriteString("\nProvider 점수는 해당 Provider의 API·클라이언트 증거만 다시 계산하고, 공통 상태 엔진·보안·릴리스 기준은 동일하게 적용합니다.\n\n")

	document.WriteString("## 등급 기준\n\n")
	document.WriteString("| 점수 | 등급 | 사용 판단 |\n")
	document.WriteString("|---:|---|---|\n")
	bands := RatingBands()
	for index, band := range bands {
		maximum := 100
		if index > 0 {
			maximum = bands[index-1].Minimum - 1
		}
		fmt.Fprintf(&document, "| %d–%d | %s | %s |\n", band.Minimum, maximum, band.Label, band.Meaning)
	}
	document.WriteString("\n가중 평균이 치명적인 결함을 숨기지 못하도록 다음 hard cap을 적용합니다.\n\n")
	document.WriteString("- API 충실도 또는 클라이언트 증거가 0점이면 최대 49점\n")
	document.WriteString("- 상태 내구성이 15/20 미만이면 최대 69점\n")
	document.WriteString("- 실패 격리·동시성이 9/15 미만이면 최대 69점\n")
	document.WriteString("- 보안·공급망이 6/10 미만이면 최대 69점\n\n")

	document.WriteString("## 세부 기준과 증거\n")
	for _, dimension := range assessment.Dimensions {
		fmt.Fprintf(&document, "\n### %s · %d/%d\n\n", dimension.Label, dimension.Earned, dimension.Weight)
		document.WriteString("| 기준 | 점수 | 상태 | 계산·판단 근거 |\n")
		document.WriteString("|---|---:|---|---|\n")
		for _, criterion := range dimension.Criteria {
			paths := make([]string, 0, len(criterion.Evidence))
			for _, evidence := range criterion.Evidence {
				paths = append(paths, fmt.Sprintf("`%s`", evidence.Path))
			}
			fmt.Fprintf(
				&document,
				"| %s | %d/%d | %s | %s 증거: %s |\n",
				criterion.Label,
				criterion.Earned,
				criterion.Weight,
				criterion.Status,
				criterion.Detail,
				strings.Join(paths, ", "),
			)
		}
	}

	document.WriteString("\n## 실행과 판정\n\n")
	document.WriteString("```bash\n")
	document.WriteString("fcp reliability --json\n")
	document.WriteString("fcp reliability --minimum 90 --json\n")
	document.WriteString("make verify\n")
	document.WriteString("```\n\n")
	document.WriteString("`fcp reliability`는 실행 중인 FCP가 제공하는 버전 관리된 평가표를 출력하고 지정한 최소 점수보다 낮으면 exit code `1`을 반환합니다. `make verify`와 CI는 평가표의 증거가 실제 테스트를 통과하는지 별도로 검사합니다.\n\n")

	document.WriteString("## 새 기능의 합격 기준\n\n")
	document.WriteString("1. 호환성 카탈로그에 지원 동작과 제외 범위를 명시합니다.\n")
	document.WriteString("2. 공식 SDK 또는 실제 wire-format 요청으로 성공 경로를 검증합니다.\n")
	document.WriteString("3. 입력 오류, not-found, 조건 실패와 부분 실패를 계약 테스트로 고정합니다.\n")
	document.WriteString("4. 상태 변경이면 저장 실패 롤백과 재시작 영속성 테스트를 추가합니다.\n")
	document.WriteString("5. 전체 Go·공식 SDK 서버 경로 커버리지 하한을 유지하거나 올립니다.\n")
	document.WriteString("6. 점수가 낮아지면 변경된 기준과 위험을 명시하며, hard cap을 우회하지 않습니다.\n\n")

	document.WriteString("## 의도적으로 보장하지 않는 범위\n\n")
	for _, limitation := range assessment.Limitations {
		fmt.Fprintf(&document, "- %s\n", limitation)
	}
	document.WriteString("\n정확한 서비스별 지원 범위는 [호환성 문서](compatibility.md)가 기준입니다.\n")
	return document.String()
}
