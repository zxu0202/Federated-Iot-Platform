// Package dataset provides streaming S1 CSV structural validation.
package dataset

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zx/federated-iot-platform/backend/internal/domain"
)

type Error struct {
	Code    string
	Field   string
	Message string
}

func (e Error) Error() string { return e.Message }

type ImportResult struct {
	StorageKey         string
	SHA256             string
	SizeBytes          int64
	Timezone           string
	UTCOffset          string
	RawRows            int64
	InvalidNumericRows int64
	NonMonotonicCount  int64
	FirstTimestamp     *time.Time
	LastTimestamp      *time.Time
}

// SourcePath resolves the immutable CSV location for one dataset. The
// configured root is the datasets directory itself; the Worker-facing storage
// key remains datasets/<dataset_id>/source.csv relative to its storage root.
// Dataset IDs are logical identifiers, not path fragments, so an import cannot
// escape the configured dataset root.
func SourcePath(root, datasetID string) (string, error) {
	if err := validateDatasetID(datasetID); err != nil {
		return "", err
	}
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve dataset root: %w", err)
	}
	target := filepath.Join(rootPath, datasetID, "source.csv")
	relative, err := filepath.Rel(rootPath, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", Error{Code: "DATASET_ID_INVALID", Field: "dataset_id", Message: "Dataset ID must resolve inside the dataset root."}
	}
	return target, nil
}

// PreflightTemporaryPath resolves the only group-writable directory beneath an
// immutable dataset. Workers may create attempt directories inside it, but do
// not receive write permission on the dataset directory or source CSV.
func PreflightTemporaryPath(root, datasetID string) (string, error) {
	source, err := SourcePath(root, datasetID)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(source), "preflight", "tmp"), nil
}

// RemoveImportedDataset removes only the files and empty directories created
// before a dataset is registered. It never recursively removes Worker output.
func RemoveImportedDataset(root, datasetID string) {
	source, err := SourcePath(root, datasetID)
	if err != nil {
		return
	}
	temporary, err := PreflightTemporaryPath(root, datasetID)
	if err != nil {
		return
	}
	_ = os.Remove(source)
	_ = os.Remove(temporary)
	_ = os.Remove(filepath.Dir(temporary))
	_ = os.Remove(filepath.Dir(source))
}

// ImportCSV writes an immutable source CSV while checking only backend-owned
// structural concerns. It never sorts, rewrites, smooths, or classifies rows.
func ImportCSV(source io.Reader, root, datasetID, requestedTimezone string, maxBytes int64) (ImportResult, error) {
	if maxBytes <= 0 {
		return ImportResult{}, fmt.Errorf("upload limit must be positive")
	}
	location, timezoneName, offset, err := resolveTimezone(requestedTimezone)
	if err != nil {
		return ImportResult{}, Error{Code: "TIME_PARSE_FAILED", Field: "timezone", Message: "Timezone must be an IANA name or UTC offset."}
	}

	target, err := SourcePath(root, datasetID)
	if err != nil {
		return ImportResult{}, err
	}
	directory := filepath.Dir(target)
	configuredRoot := filepath.Dir(directory)
	if err := makeDirectory(configuredRoot, 0o750); err != nil {
		return ImportResult{}, fmt.Errorf("create dataset root: %w", err)
	}
	if err := makeDirectory(directory, 0o750); err != nil {
		return ImportResult{}, fmt.Errorf("create dataset directory: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		return ImportResult{}, Error{Code: "DATASET_SOURCE_EXISTS", Field: "dataset_id", Message: "Dataset source already exists."}
	} else if !os.IsNotExist(err) {
		return ImportResult{}, fmt.Errorf("inspect dataset source: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			RemoveImportedDataset(root, datasetID)
		}
	}()
	preflightTemporary, err := PreflightTemporaryPath(root, datasetID)
	if err != nil {
		return ImportResult{}, err
	}
	if err := makeDirectory(filepath.Dir(preflightTemporary), 0o710); err != nil {
		return ImportResult{}, fmt.Errorf("create dataset preflight directory: %w", err)
	}
	if err := makeDirectory(preflightTemporary, 0o770); err != nil {
		return ImportResult{}, fmt.Errorf("create dataset preflight temporary directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".upload-*.tmp")
	if err != nil {
		return ImportResult{}, fmt.Errorf("create dataset temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o640); err != nil {
		return ImportResult{}, fmt.Errorf("set dataset source permissions: %w", err)
	}

	hash := sha256.New()
	limited := &io.LimitedReader{R: source, N: maxBytes + 1}
	reader := csv.NewReader(io.TeeReader(limited, io.MultiWriter(temporary, hash)))
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = true

	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return ImportResult{}, Error{Code: "CSV_EMPTY", Field: "file", Message: "CSV must include the required header and at least one data row."}
	}
	if err != nil {
		return ImportResult{}, Error{Code: "CSV_PARSE_FAILED", Field: "file", Message: "CSV header could not be parsed."}
	}
	if err := validateHeader(header); err != nil {
		return ImportResult{}, err
	}

	result := ImportResult{StorageKey: filepath.ToSlash(filepath.Join("datasets", datasetID, "source.csv")), Timezone: timezoneName, UTCOffset: offset}
	var previous *time.Time
	for {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return ImportResult{}, Error{Code: "CSV_PARSE_FAILED", Field: "file", Message: "CSV contains an invalid row."}
		}
		if len(record) != len(domain.RequiredColumns()) {
			return ImportResult{}, Error{Code: "CSV_HEADER_MISMATCH", Field: "file", Message: "Every CSV row must contain exactly seven columns."}
		}
		result.RawRows++
		parsed, parseErr := parseTimestamp(record[0], location)
		if parseErr != nil {
			return ImportResult{}, Error{Code: "TIME_PARSE_FAILED", Field: "Time_base", Message: "Time_base contains an unsupported timestamp."}
		}
		if previous != nil && parsed.Before(*previous) {
			result.NonMonotonicCount++
		}
		copyTimestamp := parsed
		previous = &copyTimestamp
		if result.FirstTimestamp == nil {
			first := parsed
			result.FirstTimestamp = &first
		}
		last := parsed
		result.LastTimestamp = &last

		invalid := false
		for index := 1; index < len(record); index++ {
			value, conversionErr := strconv.ParseFloat(strings.TrimSpace(record[index]), 64)
			if conversionErr != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				invalid = true
			}
		}
		if invalid {
			result.InvalidNumericRows++
		}
	}
	if result.RawRows == 0 {
		return ImportResult{}, Error{Code: "CSV_EMPTY", Field: "file", Message: "CSV must contain at least one data row."}
	}
	if _, err := temporary.Seek(0, io.SeekCurrent); err != nil {
		return ImportResult{}, fmt.Errorf("inspect temporary upload: %w", err)
	}
	info, err := temporary.Stat()
	if err != nil {
		return ImportResult{}, fmt.Errorf("stat temporary upload: %w", err)
	}
	if info.Size() > maxBytes {
		return ImportResult{}, Error{Code: "UPLOAD_TOO_LARGE", Field: "file", Message: "Upload exceeds the configured maximum size."}
	}
	if err := temporary.Sync(); err != nil {
		return ImportResult{}, fmt.Errorf("sync temporary upload: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return ImportResult{}, fmt.Errorf("close temporary upload: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return ImportResult{}, fmt.Errorf("atomically commit source upload: %w", err)
	}
	committed = true
	result.SizeBytes = info.Size()
	result.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return result, nil
}

func makeDirectory(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func validateHeader(header []string) error {
	required := domain.RequiredColumns()
	if len(header) != len(required) {
		return Error{Code: "CSV_HEADER_MISMATCH", Field: "file", Message: "CSV must contain exactly the approved seven columns."}
	}
	seen := make(map[string]bool, len(header))
	for index, value := range header {
		if seen[value] {
			return Error{Code: "CSV_DUPLICATE_COLUMN", Field: "file", Message: "CSV header contains a duplicate column."}
		}
		seen[value] = true
		if value != required[index] {
			return Error{Code: "CSV_HEADER_MISMATCH", Field: "file", Message: "CSV column names and order must match the approved seven-column schema."}
		}
	}
	return nil
}

func validateDatasetID(datasetID string) error {
	if datasetID == "" || datasetID == "." || datasetID == ".." || filepath.IsAbs(datasetID) || filepath.Base(datasetID) != datasetID || strings.ContainsAny(datasetID, `\\/`) {
		return Error{Code: "DATASET_ID_INVALID", Field: "dataset_id", Message: "Dataset ID must be one non-empty path-safe segment."}
	}
	return nil
}

func resolveTimezone(value string) (*time.Location, string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		location := time.Local
		_, seconds := time.Now().In(location).Zone()
		return location, location.String(), formatOffset(seconds), nil
	}
	if location, err := time.LoadLocation(value); err == nil {
		_, seconds := time.Now().In(location).Zone()
		return location, value, formatOffset(seconds), nil
	}
	parsed, err := time.Parse("-07:00", value)
	if err != nil {
		return nil, "", "", err
	}
	_, seconds := parsed.Zone()
	return time.FixedZone(value, seconds), value, formatOffset(seconds), nil
}

func formatOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}
	return fmt.Sprintf("%s%02d:%02d", sign, seconds/3600, (seconds%3600)/60)
}

func parseTimestamp(value string, location *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.000", "2006-01-02 15:04:05", "2006/01/02 15:04:05.000", "2006/01/02 15:04:05"} {
		if layout == time.RFC3339Nano {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed, nil
			}
			continue
		}
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, errors.New("unsupported timestamp")
}
