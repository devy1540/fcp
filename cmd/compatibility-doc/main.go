package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/devy1540/fcp/internal/compatibility"
	"github.com/devy1540/fcp/internal/reliability"
)

func main() {
	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	path := filepath.Join(root, "docs", "compatibility.md")
	if err := os.WriteFile(path, []byte(compatibility.Markdown()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write compatibility document: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(path)
	reliabilityPath := filepath.Join(root, reliability.Source)
	if err := os.WriteFile(reliabilityPath, []byte(reliability.Markdown()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write reliability document: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(reliabilityPath)
}

func repositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s", dir)
		}
		dir = parent
	}
}
