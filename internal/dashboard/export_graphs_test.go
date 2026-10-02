//go:build windows

package dashboard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestExportGraphInventoryMatchesFrontend(t *testing.T) {
	configPath := filepath.Join("..", "..", "frontend", "src", "lib", "export-graph-config.js")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read frontend graph config: %v", err)
	}
	contents := string(config)

	registered := AllowedExportGraphIDs()
	registeredSet := make(map[string]struct{}, len(registered))
	for _, id := range registered {
		registeredSet[id] = struct{}{}
		if !strings.Contains(contents, id) {
			t.Errorf("registered graph %q is absent from frontend export config", id)
		}
	}

	entryIDs := regexp.MustCompile(`(?m)^\s*'([^']+)':\s*\{`).FindAllStringSubmatch(contents, -1)
	if len(entryIDs) == 0 {
		t.Fatal("frontend export config contains no graph entries")
	}
	for _, match := range entryIDs {
		if _, ok := registeredSet[match[1]]; !ok {
			t.Errorf("frontend graph %q is not registered server-side", match[1])
		}
	}
}
