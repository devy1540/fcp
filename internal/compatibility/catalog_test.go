package compatibility

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogIsValid(t *testing.T) {
	services := Services()
	if err := Validate(services); err != nil {
		t.Fatal(err)
	}
	if len(services) != 13 {
		t.Fatalf("service count=%d want=13", len(services))
	}
	aws, gcp, sdk, contract := 0, 0, 0, 0
	for _, service := range services {
		switch service.Provider {
		case "AWS":
			aws++
		case "GCP":
			gcp++
		}
		switch service.Level {
		case "SDK":
			sdk++
		case "CONTRACT":
			contract++
		}
	}
	if aws != 4 || gcp != 9 || sdk != 11 || contract != 2 {
		t.Fatalf("unexpected catalog counts aws=%d gcp=%d sdk=%d contract=%d", aws, gcp, sdk, contract)
	}
}

func TestServicesReturnsIndependentCopy(t *testing.T) {
	first := Services()
	first[0].Name = "changed"
	first[0].Operations[0].Name = "changed"
	first[0].Limitations[0] = "changed"
	second := Services()
	if second[0].Name == "changed" || second[0].Operations[0].Name == "changed" || second[0].Limitations[0] == "changed" {
		t.Fatal("Services exposed mutable catalog state")
	}
}

func TestCompatibilityDocumentIsCurrent(t *testing.T) {
	documentPath := filepath.Join("..", "..", "docs", "compatibility.md")
	document, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(document) != Markdown() {
		t.Fatalf("%s is stale; run make compatibility", documentPath)
	}
}
