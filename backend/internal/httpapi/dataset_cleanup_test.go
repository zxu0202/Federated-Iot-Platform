package httpapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zx/federated-iot-platform/backend/internal/dataset"
)

func TestRemoveDatasetSourceUsesConfiguredDatasetsRoot(t *testing.T) {
	root := t.TempDir()
	removeID := "ds_remove"
	keepID := "ds_keep"
	for _, datasetID := range []string{removeID, keepID} {
		target, err := dataset.SourcePath(root, datasetID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(datasetID), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	preflightTemporary, err := dataset.PreflightTemporaryPath(root, removeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(preflightTemporary, 0o770); err != nil {
		t.Fatal(err)
	}

	removeDatasetSource(root, removeID)
	removed, err := dataset.SourcePath(root, removeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(removed); !os.IsNotExist(err) {
		t.Fatalf("removed dataset source remains: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(removed)); !os.IsNotExist(err) {
		t.Fatalf("removed dataset directory remains: %v", err)
	}
	kept, err := dataset.SourcePath(root, keepID)
	if err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(kept); err != nil || string(contents) != keepID {
		t.Fatalf("sibling dataset was affected by cleanup: contents=%q error=%v", contents, err)
	}
}
