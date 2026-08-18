package pipeline

import (
	"os"
	"testing"
)

// TestGenerateDefaultYAML — одноразовый генератор default.yaml
// (запускается вручную: go test -run TestGenerateDefaultYAML).
func TestGenerateDefaultYAML(t *testing.T) {
	if os.Getenv("GEN_YAML") == "" {
		t.Skip("manual generator: GEN_YAML=1")
	}
	spec, err := DefaultSpec(DefaultOverrides())
	if err != nil {
		t.Fatal(err)
	}
	y, err := specJSONToYAML("default", 1, nil, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("default.yaml", []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("default.yaml written")
}
