package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseIntegrityMode(t *testing.T) {
	for input, want := range map[string]IntegrityMode{
		"":         IntegrityModeStartup,
		"startup":  IntegrityModeStartup,
		" STRICT ": IntegrityModeStrict,
	} {
		got, err := ParseIntegrityMode(input)
		if err != nil {
			t.Fatalf("ParseIntegrityMode(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("ParseIntegrityMode(%q)=%q want=%q", input, got, want)
		}
	}
	if _, err := ParseIntegrityMode("off"); err == nil {
		t.Fatal("unsupported integrity mode should fail")
	}
}

func TestStartupIntegrityModeKeepsReadPathLightweight(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.IntegrityMode() != IntegrityModeStartup {
		t.Fatalf("default integrity mode=%q want=%q", store.IntegrityMode(), IntegrityModeStartup)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	object, err := store.PutObject("assets", "hello.txt", []byte("hello"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "objects", object.File), []byte("HELLO"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, body, err := store.GetObject("assets", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "HELLO" {
		t.Fatalf("startup mode body=%q want externally changed body", body)
	}
}

func TestStrictIntegrityModeRejectsRuntimeObjectTampering(t *testing.T) {
	t.Run("S3 read", func(t *testing.T) {
		dir := t.TempDir()
		store := openStrictStore(t, dir)
		defer store.Close()
		if err := store.CreateBucket("assets"); err != nil {
			t.Fatal(err)
		}
		object, err := store.PutObject("assets", "hello.txt", []byte("hello"), "text/plain", nil)
		if err != nil {
			t.Fatal(err)
		}
		tamperObjectFile(t, dir, object.File, []byte("HELLO"))
		if _, _, err := store.GetObject("assets", "hello.txt"); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("strict S3 read error=%v want=%v", err, ErrStateCorrupt)
		}
	})

	t.Run("GCS read", func(t *testing.T) {
		dir := t.TempDir()
		store := openStrictStore(t, dir)
		defer store.Close()
		if _, err := store.CreateGCSBucket("test-project", "assets", "ASIA-NORTHEAST3", "STANDARD"); err != nil {
			t.Fatal(err)
		}
		object, err := store.PutGCSObject("assets", "hello.txt", []byte("hello"), "text/plain", nil)
		if err != nil {
			t.Fatal(err)
		}
		tamperObjectFile(t, dir, object.File, []byte("HELLO"))
		if _, _, err := store.GCSObject("assets", "hello.txt"); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("strict GCS read error=%v want=%v", err, ErrStateCorrupt)
		}
	})

	t.Run("multipart completion", func(t *testing.T) {
		dir := t.TempDir()
		store := openStrictStore(t, dir)
		defer store.Close()
		if err := store.CreateBucket("assets"); err != nil {
			t.Fatal(err)
		}
		upload, err := store.CreateMultipartUpload("assets", "large.bin", "application/octet-stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		part, err := store.UploadMultipartPart("assets", "large.bin", upload.ID, 1, []byte("hello"))
		if err != nil {
			t.Fatal(err)
		}
		tamperObjectFile(t, dir, part.File, []byte("HELLO"))
		if _, err := store.CompleteMultipartUpload("assets", "large.bin", upload.ID, []CompletedMultipartPart{{PartNumber: 1, ETag: part.ETag}}); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("strict multipart completion error=%v want=%v", err, ErrStateCorrupt)
		}
	})

	t.Run("snapshot save", func(t *testing.T) {
		dir := t.TempDir()
		store := openStrictStore(t, dir)
		defer store.Close()
		if err := store.CreateBucket("assets"); err != nil {
			t.Fatal(err)
		}
		object, err := store.PutObject("assets", "hello.txt", []byte("hello"), "text/plain", nil)
		if err != nil {
			t.Fatal(err)
		}
		tamperObjectFile(t, dir, object.File, []byte("HELLO"))
		if _, err := store.SaveSnapshot("corrupt"); !errors.Is(err, ErrStateCorrupt) {
			t.Fatalf("strict snapshot error=%v want=%v", err, ErrStateCorrupt)
		}
	})
}

func TestOpenWithOptionsRejectsInvalidIntegrityModeBeforeCreatingData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if _, err := OpenWithOptions(dir, OpenOptions{IntegrityMode: "invalid"}); err == nil {
		t.Fatal("invalid integrity mode should fail")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid mode created data directory: %v", err)
	}
}

func BenchmarkGetObjectIntegrityModes(b *testing.B) {
	body := make([]byte, 1<<20)
	for _, mode := range []IntegrityMode{IntegrityModeStartup, IntegrityModeStrict} {
		b.Run(string(mode), func(b *testing.B) {
			store, err := OpenWithOptions(b.TempDir(), OpenOptions{IntegrityMode: mode})
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			if err := store.CreateBucket("assets"); err != nil {
				b.Fatal(err)
			}
			if _, err := store.PutObject("assets", "object.bin", body, "application/octet-stream", nil); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, _, err := store.GetObject("assets", "object.bin"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func openStrictStore(t *testing.T, dir string) *Store {
	t.Helper()
	store, err := OpenWithOptions(dir, OpenOptions{IntegrityMode: IntegrityModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	if store.IntegrityMode() != IntegrityModeStrict {
		t.Fatalf("integrity mode=%q want=%q", store.IntegrityMode(), IntegrityModeStrict)
	}
	return store
}

func tamperObjectFile(t *testing.T, dir, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "objects", name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}
