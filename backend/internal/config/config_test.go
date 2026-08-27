package config

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBuildsDatabaseURLFromPasswordFile(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "database-password.txt")
	if err := os.WriteFile(passwordFile, []byte("local:p@ssword\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IOT_DATABASE_URL", "")
	t.Setenv("IOT_DATABASE_HOST", "postgres")
	t.Setenv("IOT_DATABASE_PORT", "5432")
	t.Setenv("IOT_DATABASE_NAME", "federated_iot")
	t.Setenv("IOT_DATABASE_USER", "web_api")
	t.Setenv("IOT_DATABASE_PASSWORD_FILE", passwordFile)
	t.Setenv("IOT_DATABASE_SSLMODE", "disable")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	password, present := parsed.User.Password()
	if parsed.Host != "postgres:5432" || parsed.User.Username() != "web_api" || !present || password != "local:p@ssword" {
		t.Fatalf("structured database URL was not encoded safely: %s", parsed.Redacted())
	}
}

func TestLoadRejectsPartialStructuredDatabaseConfiguration(t *testing.T) {
	t.Setenv("IOT_DATABASE_URL", "")
	t.Setenv("IOT_DATABASE_HOST", "postgres")
	t.Setenv("IOT_DATABASE_NAME", "")
	t.Setenv("IOT_DATABASE_USER", "")
	t.Setenv("IOT_DATABASE_PASSWORD_FILE", "")
	if _, err := Load(); err == nil {
		t.Fatal("partial structured database configuration was accepted")
	}
}

func TestLoadBuildsDedicatedMigrationDatabaseURLFromPasswordFile(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "migration-password.txt")
	if err := os.WriteFile(passwordFile, []byte("migration-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IOT_DATABASE_URL", "postgres://web_api:secret@postgres/federated_iot?sslmode=disable")
	t.Setenv("IOT_MIGRATION_DATABASE_URL", "")
	t.Setenv("IOT_MIGRATION_DATABASE_HOST", "postgres")
	t.Setenv("IOT_MIGRATION_DATABASE_PORT", "5432")
	t.Setenv("IOT_MIGRATION_DATABASE_NAME", "federated_iot")
	t.Setenv("IOT_MIGRATION_DATABASE_USER", "platform_migrator")
	t.Setenv("IOT_MIGRATION_DATABASE_PASSWORD_FILE", passwordFile)
	t.Setenv("IOT_MIGRATION_DATABASE_SSLMODE", "disable")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(cfg.MigrationDatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.User.Username() != "platform_migrator" || parsed.Host != "postgres:5432" {
		t.Fatalf("migration URL was not built for the dedicated role: %s", parsed.Redacted())
	}
}

func TestLoadDefaultsWebAPIToAllInterfaces(t *testing.T) {
	configureHTTPTestDatabase(t)
	t.Setenv("IOT_HTTP_ADDRESS", "")
	t.Setenv("PORT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddress != "0.0.0.0:8080" {
		t.Fatalf("default HTTP address = %q, want all-interface default", cfg.HTTPAddress)
	}
}

func TestLoadUsesPortWhenNoExplicitHTTPAddressExists(t *testing.T) {
	configureHTTPTestDatabase(t)
	t.Setenv("IOT_HTTP_ADDRESS", "")
	t.Setenv("PORT", "18080")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddress != "0.0.0.0:18080" {
		t.Fatalf("PORT-derived HTTP address = %q, want %q", cfg.HTTPAddress, "0.0.0.0:18080")
	}
}

func TestLoadUsesExplicitHTTPAddressBeforePort(t *testing.T) {
	configureHTTPTestDatabase(t)
	t.Setenv("IOT_HTTP_ADDRESS", "api.example.test:19091")
	t.Setenv("PORT", "18080")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddress != "api.example.test:19091" {
		t.Fatalf("explicit HTTP address = %q, want %q", cfg.HTTPAddress, "api.example.test:19091")
	}
}

func TestLoadRejectsInvalidHTTPAddressesAndPorts(t *testing.T) {
	tests := []struct {
		name    string
		address string
		port    string
	}{
		{name: "missing address port", address: "0.0.0.0", port: "18080"},
		{name: "empty address host", address: ":18080", port: "18080"},
		{name: "address port zero", address: "0.0.0.0:0", port: "18080"},
		{name: "address port too large", address: "0.0.0.0:65536", port: "18080"},
		{name: "address whitespace", address: " 0.0.0.0:18080", port: "18080"},
		{name: "invalid port text", address: "", port: "http"},
		{name: "invalid port zero", address: "", port: "0"},
		{name: "invalid port too large", address: "", port: "65536"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configureHTTPTestDatabase(t)
			t.Setenv("IOT_HTTP_ADDRESS", test.address)
			t.Setenv("PORT", test.port)
			if _, err := Load(); err == nil {
				t.Fatal("invalid HTTP listener configuration was accepted")
			}
		})
	}
}

func configureHTTPTestDatabase(t *testing.T) {
	t.Helper()
	t.Setenv("IOT_DATABASE_URL", "postgres://web_api:secret@postgres/federated_iot?sslmode=disable")
	t.Setenv("IOT_MIGRATION_DATABASE_URL", "postgres://platform_migrator:secret@postgres/federated_iot?sslmode=disable")
}
