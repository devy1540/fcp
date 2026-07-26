package compatibility

import (
	"fmt"
	"strings"
)

const dashboardDescription = "`/_fcp/ui`에서 AWS와 GCP를 필터링하고 서비스별 런타임 상태, 검증 등급, 지원 동작과 제외 범위를 확인할 수 있습니다. `READY`는 현재 프로세스가 요청에 응답할 수 있다는 뜻이며 전체 클라우드 동등성을 뜻하지 않습니다. 대시보드는 Secret payload, DynamoDB item, key material, 메시지 본문, AI 프롬프트와 생성 결과를 노출하지 않습니다."

func Markdown() string {
	var document strings.Builder
	document.WriteString("# 호환성 범위\n\n")
	document.WriteString("이 문서는 FCP가 검증한 동작과 아직 구현하지 않은 동작을 구분합니다. `FULL`은 적힌 범위의 요청·응답과 핵심 상태 전환을 테스트했다는 의미이고, `PARTIAL`은 명시된 제약이 있다는 의미입니다. 어느 등급도 실제 클라우드 전체와의 완전한 동일성을 보장하지 않습니다.\n\n")
	document.WriteString("이 파일의 서비스별 표는 `internal/compatibility` 카탈로그에서 생성됩니다. 변경 후 `make compatibility`로 갱신하며 테스트가 문서 드리프트를 검사합니다.\n\n")
	document.WriteString("## 로컬 관리 대시보드\n\n")
	document.WriteString(dashboardDescription)
	document.WriteString("\n\n## 서비스별 검증 매트릭스\n")
	for _, service := range Services() {
		fmt.Fprintf(&document, "\n### %s · %s\n\n", service.Provider, service.Name)
		fmt.Fprintf(&document, "- 검증 방식: `%s`\n", VerificationLabel(service.Level))
		fmt.Fprintf(&document, "- 검증 근거: %s\n\n", service.Evidence)
		document.WriteString("| API/동작 | 상태 | 검증 범위 |\n")
		document.WriteString("|---|---|---|\n")
		for _, operation := range service.Operations {
			fmt.Fprintf(
				&document,
				"| %s | %s | %s |\n",
				escapeMarkdownTable(operation.Name),
				operation.Status,
				escapeMarkdownTable(operation.Scope),
			)
		}
		document.WriteString("\n제외 범위:\n")
		for _, limitation := range service.Limitations {
			fmt.Fprintf(&document, "- %s\n", limitation)
		}
	}
	document.WriteString("\n## 공통 차이와 실행 안전성\n\n")
	document.WriteString("- AWS 계정 ID는 `000000000000`, 리전은 `us-east-1`로 고정됩니다.\n")
	document.WriteString("- SigV4 서명과 자격 증명은 검증하지 않습니다.\n")
	document.WriteString("- 메타데이터는 JSON snapshot, 객체 본문은 별도 로컬 파일에 저장합니다.\n")
	document.WriteString("- 동일 데이터 디렉터리는 한 프로세스만 열 수 있으며 두 번째 writer는 시작 단계에서 거부됩니다.\n")
	document.WriteString("- `startup` 무결성 모드는 시작 시 객체 SHA-256을 검사하고, 선택적인 `strict` 모드는 실행 중 객체 읽기와 스냅샷 저장 시 다시 검사합니다.\n")
	document.WriteString("- 다중 노드·리전 장애, 실제 서비스의 성능·quota·분산 일관성은 재현하지 않습니다.\n")
	return document.String()
}

func escapeMarkdownTable(value string) string {
	return strings.ReplaceAll(value, "|", `\|`)
}
