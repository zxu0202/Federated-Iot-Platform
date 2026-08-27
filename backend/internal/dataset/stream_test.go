package dataset

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestImportCSVStreamsStrictHeaderAndPreservesBytes(t *testing.T) {
	root := t.TempDir()
	source := []byte("Time_base,dzdl_1,dzdl_2,dzdl_3,dzdl_4,zl,sd\n2026-08-16 00:00:02,1,2,3,4,5,6\n2026-08-16 00:00:01,1,2,3,4,5,6\n")
	result, err := ImportCSV(bytes.NewReader(source), root, "ds_test", "Asia/Shanghai", int64(len(source)+1))
	if err != nil {
		t.Fatal(err)
	}
	if result.RawRows != 2 || result.NonMonotonicCount != 1 || result.InvalidNumericRows != 0 {
		t.Fatalf("unexpected statistics: %#v", result)
	}
	if result.StorageKey != "datasets/ds_test/source.csv" {
		t.Fatalf("unexpected storage key: %q", result.StorageKey)
	}
	target, err := SourcePath(root, "ds_test")
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join(root, "ds_test", "source.csv") {
		t.Fatalf("source path = %q, want direct path below configured dataset root", target)
	}
	stored, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, stored) {
		t.Fatal("source bytes were changed during import")
	}
	preflightTemporary, err := PreflightTemporaryPath(root, "ds_test")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Dir(target), filepath.Dir(preflightTemporary), preflightTemporary} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("required dataset directory %q was not created: %v", path, err)
		}
	}
	if runtime.GOOS != "windows" {
		assertPermission(t, target, 0o640)
		assertPermission(t, filepath.Dir(target), 0o750)
		assertPermission(t, filepath.Dir(preflightTemporary), 0o710)
		assertPermission(t, preflightTemporary, 0o770)
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".upload-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("successful import left temporary files: %v", temporary)
	}
}

func TestImportCSVRejectsHeaderAndEnforcesLimit(t *testing.T) {
	badHeader := []byte("Time_base,dzdl_1,dzdl_2,dzdl_3,dzdl_4,sd,zl\n2026-08-16 00:00:00,1,2,3,4,5,6\n")
	if _, err := ImportCSV(bytes.NewReader(badHeader), t.TempDir(), "ds_bad", "Asia/Shanghai", 1024); err == nil {
		t.Fatal("out-of-order header was accepted")
	}
	valid := []byte("Time_base,dzdl_1,dzdl_2,dzdl_3,dzdl_4,zl,sd\n2026-08-16 00:00:00,1,2,3,4,5,6\n")
	if _, err := ImportCSV(bytes.NewReader(valid), t.TempDir(), "ds_large", "Asia/Shanghai", int64(len(valid)-1)); err == nil {
		t.Fatal("oversize file was accepted")
	}
}

func TestImportCSVCountsInvalidNumbersWithoutPreprocessing(t *testing.T) {
	source := []byte("Time_base,dzdl_1,dzdl_2,dzdl_3,dzdl_4,zl,sd\n2026-08-16 00:00:00,1,NULL,3,4,5,6\n")
	result, err := ImportCSV(bytes.NewReader(source), t.TempDir(), "ds_invalid", "Asia/Shanghai", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if result.InvalidNumericRows != 1 {
		t.Fatalf("expected one invalid numeric row, got %d", result.InvalidNumericRows)
	}
}

func TestImportCSVRejectsDatasetPathEscape(t *testing.T) {
	root := t.TempDir()
	source := []byte("Time_base,dzdl_1,dzdl_2,dzdl_3,dzdl_4,zl,sd\n2026-08-16 00:00:00,1,2,3,4,5,6\n")
	for _, datasetID := range []string{"../outside", "nested/child", "nested\\child", filepath.Join(root, "absolute")} {
		_, err := ImportCSV(bytes.NewReader(source), root, datasetID, "Asia/Shanghai", 1024)
		var importError Error
		if !errors.As(err, &importError) || importError.Code != "DATASET_ID_INVALID" {
			t.Fatalf("dataset ID %q returned %v; want DATASET_ID_INVALID", datasetID, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "outside")); !os.IsNotExist(err) {
		t.Fatalf("path escape created content outside configured dataset root: %v", err)
	}
}

func TestImportCSVFailureLeavesNoCommittedSourceOrTemporaryFile(t *testing.T) {
	root := t.TempDir()
	invalid := []byte("Time_base,dzdl_1,dzdl_2,dzdl_3,dzdl_4,sd,zl\n2026-08-16 00:00:00,1,2,3,4,5,6\n")
	if _, err := ImportCSV(bytes.NewReader(invalid), root, "ds_atomic", "Asia/Shanghai", 1024); err == nil {
		t.Fatal("invalid import unexpectedly succeeded")
	}
	target, err := SourcePath(root, "ds_atomic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("failed import committed source file: %v", err)
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".upload-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("failed import left temporary files: %v", temporary)
	}
	if _, err := os.Stat(filepath.Dir(target)); !os.IsNotExist(err) {
		t.Fatalf("failed import left a dataset directory: %v", err)
	}
}

func assertPermission(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("permissions for %q = %04o, want %04o", path, got, want)
	}
}

func TestImportCSVDeniesOverwriteOfImmutableSource(t *testing.T) {
	root := t.TempDir()
	first := []byte("Time_base,dzdl_1,dzdl_2,dzdl_3,dzdl_4,zl,sd\n2026-08-16 00:00:00,1,2,3,4,5,6\n")
	second := []byte("Time_base,dzdl_1,dzdl_2,dzdl_3,dzdl_4,zl,sd\n2026-08-16 00:00:00,6,5,4,3,2,1\n")
	if _, err := ImportCSV(bytes.NewReader(first), root, "ds_immutable", "Asia/Shanghai", 1024); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportCSV(bytes.NewReader(second), root, "ds_immutable", "Asia/Shanghai", 1024); err == nil {
		t.Fatal("second import unexpectedly overwrote immutable source")
	} else {
		var importError Error
		if !errors.As(err, &importError) || importError.Code != "DATASET_SOURCE_EXISTS" {
			t.Fatalf("second import returned %v; want DATASET_SOURCE_EXISTS", err)
		}
	}
	target, err := SourcePath(root, "ds_immutable")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, stored) {
		t.Fatal("immutable source was modified")
	}
}
