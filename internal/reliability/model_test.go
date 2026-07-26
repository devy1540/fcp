package reliability

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devy1540/fcp/internal/compatibility"
)

func TestCurrentAssessmentIsValidAndMeetsRecommendation(t *testing.T) {
	assessment := Assess(compatibility.Services())
	if err := Validate(assessment); err != nil {
		t.Fatal(err)
	}
	if assessment.Score != 92 || assessment.Rating != "HIGH" || !assessment.Passed {
		t.Fatalf("unexpected current assessment: %+v", assessment)
	}
	if len(assessment.Dimensions) != 6 || len(assessment.Providers) != 2 {
		t.Fatalf("unexpected assessment shape: %+v", assessment)
	}
	assertProviderScore(t, assessment, "AWS", 88)
	assertProviderScore(t, assessment, "GCP", 94)
}

func TestCriticalDimensionsCapAnOtherwiseHighScore(t *testing.T) {
	dimensions := assessmentDimensions(compatibility.Services())
	for index := range dimensions {
		if dimensions[index].ID == "state_safety" {
			dimensions[index].Earned = 10
			break
		}
	}
	score, caps := scoreDimensions(dimensions)
	if score != 69 || len(caps) != 1 || caps[0].Dimension != "state_safety" {
		t.Fatalf("critical score cap was not applied: score=%d caps=%+v", score, caps)
	}
}

func TestValidateRejectsTamperedAssessment(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Assessment)
	}{
		{name: "score", mutate: func(assessment *Assessment) { assessment.Score++ }},
		{name: "dimension weight", mutate: func(assessment *Assessment) { assessment.Dimensions[0].Weight-- }},
		{name: "criterion points", mutate: func(assessment *Assessment) { assessment.Dimensions[0].Criteria[0].Earned-- }},
		{name: "provider rating", mutate: func(assessment *Assessment) { assessment.Providers[0].Rating = "VERY_HIGH" }},
		{name: "limitations", mutate: func(assessment *Assessment) { assessment.Limitations = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assessment := Assess(compatibility.Services())
			test.mutate(&assessment)
			if err := Validate(assessment); err == nil {
				t.Fatal("tampered assessment was accepted")
			}
		})
	}
}

func TestEvidencePathsExist(t *testing.T) {
	assessment := Assess(compatibility.Services())
	root := filepath.Join("..", "..")
	seen := map[string]bool{}
	for _, dimension := range assessment.Dimensions {
		for _, criterion := range dimension.Criteria {
			for _, evidence := range criterion.Evidence {
				if seen[evidence.Path] {
					continue
				}
				seen[evidence.Path] = true
				if _, err := os.Stat(filepath.Join(root, evidence.Path)); err != nil {
					t.Fatalf("evidence path %s: %v", evidence.Path, err)
				}
			}
		}
	}
}

func TestReliabilityDocumentIsCurrent(t *testing.T) {
	documentPath := filepath.Join("..", "..", Source)
	document, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(document) != Markdown() {
		t.Fatalf("%s is stale; run make compatibility", documentPath)
	}
}

func assertProviderScore(t *testing.T, assessment Assessment, provider string, score int) {
	t.Helper()
	for _, candidate := range assessment.Providers {
		if candidate.Provider == provider {
			if candidate.Score != score {
				t.Fatalf("%s score=%d want=%d", provider, candidate.Score, score)
			}
			return
		}
	}
	t.Fatalf("provider %s is missing", provider)
}
