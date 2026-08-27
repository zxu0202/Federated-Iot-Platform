// Package config loads the Web/API process configuration.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultUploadLimitBytes  int64 = 500 * 1024 * 1024
	DefaultLeaseDuration           = 60 * time.Second
	DefaultHeartbeatInterval       = 10 * time.Second
	DefaultShutdownTimeout         = 30 * time.Second
	defaultHTTPPort                = 8080
)

// Config is the approved S1 PostgreSQL deployment profile.
// It deliberately contains no SQLite fields or fallback behavior.
type Config struct {
	HTTPAddress              string
	DatabaseURL              string
	MigrationDatabaseURL     string
	DatasetRoot              string
	ArtifactRoot             string
	StaticRoot               string
	UploadLimitBytes         int64
	LeaseDuration            time.Duration
	HeartbeatInterval        time.Duration
	ShutdownTimeout          time.Duration
	ServiceVersion           string
	WorkerContract           string
	AlgorithmVersion         string
	WorkerVersion            string
	WorkerImageDigest        string
	NumericRuntime           string
	ParameterConstraintsFile string
}

// Load reads only explicit environment variables and rejects unsafe values.
func Load() (Config, error) {
	httpAddress, err := httpAddressFromEnvironment()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		HTTPAddress:              httpAddress,
		DatabaseURL:              strings.TrimSpace(os.Getenv("IOT_DATABASE_URL")),
		MigrationDatabaseURL:     strings.TrimSpace(os.Getenv("IOT_MIGRATION_DATABASE_URL")),
		DatasetRoot:              valueOr("IOT_DATASET_ROOT", "./data"),
		ArtifactRoot:             valueOr("IOT_ARTIFACT_ROOT", "./data"),
		StaticRoot:               strings.TrimSpace(os.Getenv("IOT_STATIC_ROOT")),
		UploadLimitBytes:         DefaultUploadLimitBytes,
		LeaseDuration:            DefaultLeaseDuration,
		HeartbeatInterval:        DefaultHeartbeatInterval,
		ShutdownTimeout:          DefaultShutdownTimeout,
		ServiceVersion:           valueOr("IOT_SERVICE_VERSION", "dev"),
		WorkerContract:           "worker.task.v1",
		AlgorithmVersion:         strings.TrimSpace(os.Getenv("IOT_ALGORITHM_VERSION")),
		WorkerVersion:            strings.TrimSpace(os.Getenv("IOT_WORKER_VERSION")),
		WorkerImageDigest:        strings.TrimSpace(os.Getenv("IOT_WORKER_IMAGE_DIGEST")),
		NumericRuntime:           strings.TrimSpace(os.Getenv("IOT_NUMERIC_RUNTIME")),
		ParameterConstraintsFile: strings.TrimSpace(os.Getenv("IOT_PARAMETER_CONSTRAINTS_FILE")),
	}
	if cfg.DatabaseURL == "" {
		databaseURL, err := databaseURLFromSecret()
		if err != nil {
			return Config{}, err
		}
		cfg.DatabaseURL = databaseURL
	}
	if cfg.MigrationDatabaseURL == "" {
		migrationURL, err := migrationDatabaseURLFromSecret()
		if err != nil {
			return Config{}, err
		}
		cfg.MigrationDatabaseURL = migrationURL
	}

	if raw := strings.TrimSpace(os.Getenv("IOT_UPLOAD_LIMIT_BYTES")); raw != "" {
		limit, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || limit <= 0 || limit > DefaultUploadLimitBytes {
			return Config{}, fmt.Errorf("IOT_UPLOAD_LIMIT_BYTES must be between 1 and %d", DefaultUploadLimitBytes)
		}
		cfg.UploadLimitBytes = limit
	}
	if raw := strings.TrimSpace(os.Getenv("IOT_LEASE_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 10 || seconds > 600 {
			return Config{}, fmt.Errorf("IOT_LEASE_SECONDS must be between 10 and 600")
		}
		cfg.LeaseDuration = time.Duration(seconds) * time.Second
	}
	if raw := strings.TrimSpace(os.Getenv("IOT_HEARTBEAT_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 1 || time.Duration(seconds)*time.Second >= cfg.LeaseDuration {
			return Config{}, fmt.Errorf("IOT_HEARTBEAT_SECONDS must be positive and less than the lease duration")
		}
		cfg.HeartbeatInterval = time.Duration(seconds) * time.Second
	}

	for _, root := range []string{cfg.DatasetRoot, cfg.ArtifactRoot, cfg.StaticRoot} {
		if root == "" {
			continue
		}
		if !filepath.IsAbs(root) {
			continue
		}
		if filepath.Clean(root) != root {
			return Config{}, fmt.Errorf("storage root must be normalized: %q", root)
		}
	}
	return cfg, nil
}

// httpAddressFromEnvironment keeps the process reachable by default while
// allowing a deployment to deliberately select a different valid address.
// IOT_HTTP_ADDRESS has priority over PORT because it includes both host and
// port; PORT only selects the port for the all-interface default host.
func httpAddressFromEnvironment() (string, error) {
	if address, present := os.LookupEnv("IOT_HTTP_ADDRESS"); present && address != "" {
		return validateHTTPAddress(address)
	}

	port := defaultHTTPPort
	if value, present := os.LookupEnv("PORT"); present && value != "" {
		parsed, err := parseHTTPPort(value, "PORT")
		if err != nil {
			return "", err
		}
		port = parsed
	}
	return net.JoinHostPort("0.0.0.0", strconv.Itoa(port)), nil
}

func validateHTTPAddress(address string) (string, error) {
	if address != strings.TrimSpace(address) {
		return "", fmt.Errorf("IOT_HTTP_ADDRESS must not contain leading or trailing whitespace")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || !validHTTPHost(host) {
		return "", fmt.Errorf("IOT_HTTP_ADDRESS must be a valid host:port address")
	}
	parsedPort, err := parseHTTPPort(port, "IOT_HTTP_ADDRESS port")
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(parsedPort)), nil
}

func validHTTPHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func parseHTTPPort(value, name string) (int, error) {
	if value != strings.TrimSpace(value) || value == "" {
		return 0, fmt.Errorf("%s must be a decimal port between 1 and 65535", name)
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("%s must be a decimal port between 1 and 65535", name)
		}
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be a decimal port between 1 and 65535", name)
	}
	return port, nil
}

func databaseURLFromSecret() (string, error) {
	return databaseURLFromSecretPrefix("IOT_DATABASE")
}

func migrationDatabaseURLFromSecret() (string, error) {
	return databaseURLFromSecretPrefix("IOT_MIGRATION_DATABASE")
}

func databaseURLFromSecretPrefix(prefix string) (string, error) {
	host := strings.TrimSpace(os.Getenv(prefix + "_HOST"))
	name := strings.TrimSpace(os.Getenv(prefix + "_NAME"))
	user := strings.TrimSpace(os.Getenv(prefix + "_USER"))
	passwordFile := strings.TrimSpace(os.Getenv(prefix + "_PASSWORD_FILE"))
	if host == "" && name == "" && user == "" && passwordFile == "" {
		return "", nil
	}
	if host == "" || name == "" || user == "" || passwordFile == "" {
		return "", fmt.Errorf("%s structured configuration requires host, name, user, and password file", prefix)
	}
	passwordBytes, err := os.ReadFile(passwordFile)
	if err != nil {
		return "", fmt.Errorf("%s_PASSWORD_FILE cannot be read", prefix)
	}
	password := strings.TrimSpace(string(passwordBytes))
	if password == "" {
		return "", fmt.Errorf("%s_PASSWORD_FILE is empty", prefix)
	}
	port := valueOr(prefix+"_PORT", "5432")
	if parsedPort, err := strconv.Atoi(port); err != nil || parsedPort < 1 || parsedPort > 65535 {
		return "", fmt.Errorf("%s_PORT must be between 1 and 65535", prefix)
	}
	sslMode := valueOr(prefix+"_SSLMODE", "disable")
	if sslMode != "disable" && sslMode != "require" && sslMode != "verify-ca" && sslMode != "verify-full" {
		return "", fmt.Errorf("%s_SSLMODE is invalid", prefix)
	}
	databaseURL := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, port),
		Path:     "/" + name,
		RawQuery: "sslmode=" + url.QueryEscape(sslMode),
	}
	return databaseURL.String(), nil
}

var ociDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ValidateSimulationRuntime rejects incomplete runtime traceability before the
// Web/API can accept a simulation. Migration-only use does not require it.
func (cfg Config) ValidateSimulationRuntime() error {
	if cfg.AlgorithmVersion == "" || cfg.WorkerVersion == "" || cfg.NumericRuntime == "" {
		return fmt.Errorf("IOT_ALGORITHM_VERSION, IOT_WORKER_VERSION, and IOT_NUMERIC_RUNTIME are required for simulation admission")
	}
	if !ociDigestPattern.MatchString(cfg.WorkerImageDigest) {
		return fmt.Errorf("IOT_WORKER_IMAGE_DIGEST must be an immutable sha256:<64 lowercase hex> OCI digest")
	}
	return nil
}

func valueOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
