package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type publishedWireFixtures struct {
	Source struct {
		Tag                 string `json:"tag"`
		Commit              string `json:"commit"`
		WireProtocolVersion int    `json:"wireProtocolVersion"`
	} `json:"source"`
	SchemaVersion int `json:"schemaVersion"`
}

func TestWireCompatibilityMetadata(t *testing.T) {
	if WireProtocolVersion < MinSupportedClientWireVersion {
		t.Errorf("current wire version %d is below client floor %d", WireProtocolVersion, MinSupportedClientWireVersion)
	}
	if WireIncompatibleCloseCode < 4000 || WireIncompatibleCloseCode > 4999 {
		t.Errorf("incompatible close code %d is outside the private application range", WireIncompatibleCloseCode)
	}

	path := filepath.Join("..", "wire-golden", "v3-published.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read previous published fixture manifest: %v", err)
	}
	var fixtures publishedWireFixtures
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("decode previous published fixture manifest: %v", err)
	}
	if fixtures.SchemaVersion != 1 {
		t.Errorf("fixture manifest schema = %d, want 1", fixtures.SchemaVersion)
	}
	if fixtures.Source.Tag != "v2.8.0" {
		t.Errorf("fixture source tag = %q, want v2.8.0", fixtures.Source.Tag)
	}
	if fixtures.Source.Commit == "" {
		t.Error("fixture source commit is empty")
	}
	if fixtures.Source.WireProtocolVersion != MinSupportedClientWireVersion {
		t.Errorf("published fixture revision = %d, server client floor = %d", fixtures.Source.WireProtocolVersion, MinSupportedClientWireVersion)
	}
}
