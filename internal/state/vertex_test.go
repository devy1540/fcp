package state

import "testing"

func TestVertexGenerationPersistsAndWorkloadResetClearsMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordVertexGeneration("fcp-local", "global", "gemini-2.5-flash", "generateContent", 42, 1); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	generations := reopened.ListVertexGenerations("fcp-local")
	if len(generations) != 1 || generations[0].InputCharacters != 42 || generations[0].ToolCount != 1 {
		t.Fatalf("unexpected persisted generation metadata: %+v", generations)
	}
	if err := reopened.ResetWorkloadData(); err != nil {
		t.Fatal(err)
	}
	if generations := reopened.ListVertexGenerations(""); len(generations) != 0 {
		t.Fatalf("workload reset did not clear generation metadata: %+v", generations)
	}
}
