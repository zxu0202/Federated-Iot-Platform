package healthcheck

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestLiveHealthcheckDoesNotReadConfigOrRequireReadiness(t *testing.T) {
	var output bytes.Buffer
	err := Execute([]string{"--kind=live", "--config", "/path/that/does/not/exist/platform.yaml"}, &output)
	if err != nil {
		t.Fatalf("live healthcheck failed: %v", err)
	}
	if output.String() != "{\"kind\":\"live\",\"service\":\"web-api\",\"status\":\"alive\"}\n" {
		t.Fatalf("unexpected live healthcheck output: %q", output.String())
	}
}

func TestReadyHealthcheckRequiresRunningHTTPService(t *testing.T) {
	err := Execute([]string{"--kind=ready", "--config=/etc/federated-iot/platform.yaml"}, &bytes.Buffer{})
	if !errors.Is(err, ErrReadyRequiresHTTP) {
		t.Fatalf("ready healthcheck error = %v, want ErrReadyRequiresHTTP", err)
	}
}

func TestHealthcheckRejectsMissingConfigAndUnknownKind(t *testing.T) {
	if err := Execute([]string{"--kind=live"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--config") {
		t.Fatalf("missing config error = %v", err)
	}
	if err := Execute([]string{"--kind=task", "--config=/etc/federated-iot/platform.yaml"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unknown kind error = %v", err)
	}
}
