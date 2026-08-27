package httpapi

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/zx/federated-iot-platform/backend/internal/domain"
)

func TestCancellationHTTPStatusPreservesQueuedAndActiveSemantics(t *testing.T) {
	tests := []struct {
		name   string
		status domain.SimulationStatus
		want   int
	}{
		{name: "queued cancellation is terminal", status: domain.SimulationCancelled, want: http.StatusOK},
		{name: "running cancellation is accepted", status: domain.SimulationCancelling, want: http.StatusAccepted},
		{name: "cancelled retry is idempotent", status: domain.SimulationCancelled, want: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := cancellationHTTPStatus(test.status); got != test.want {
				t.Fatalf("cancellationHTTPStatus(%q) = %d, want %d", test.status, got, test.want)
			}
		})
	}
}

func TestTerminalCancellationRemainsConflict(t *testing.T) {
	if got := statusForCode("RUN_NOT_CANCELLABLE"); got != http.StatusConflict {
		t.Fatalf("RUN_NOT_CANCELLABLE status = %d, want %d", got, http.StatusConflict)
	}
	if got := statusForCode("RUN_NOT_RERUNNABLE"); got != http.StatusConflict {
		t.Fatalf("RUN_NOT_RERUNNABLE status = %d, want %d", got, http.StatusConflict)
	}
}

func TestCompletedDetailUsesTheSharedResultReadGate(t *testing.T) {
	contents, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	api := string(contents)
	start := strings.Index(api, "func (api *API) getSimulation(")
	end := strings.Index(api[start:], "func (api *API) rerunSimulation(")
	if start < 0 || end < 0 {
		t.Fatal("simulation detail handler boundaries are missing")
	}
	detail := api[start : start+end]
	if !strings.Contains(detail, "if record.Status == domain.SimulationCompleted {") || !strings.Contains(detail, "api.repo.RequireCompletedArtifacts(request.Context(), record.RunID)") {
		t.Fatal("completed simulation detail does not use the shared result-read gate")
	}
}

func TestCancellationHandlerUsesStableErrorBoundary(t *testing.T) {
	contents, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	api := string(contents)
	start := strings.Index(api, "func (api *API) cancelSimulation(")
	end := strings.Index(api[start:], "func cancellationHTTPStatus(")
	if start < 0 || end < 0 {
		t.Fatal("cancellation handler boundaries are missing")
	}
	handler := api[start : start+end]
	if !strings.Contains(handler, "api.writeError(writer, request, err)") {
		t.Fatal("cancellation handler bypasses stable error/request_id response handling")
	}
	if !strings.Contains(handler, "cancellationHTTPStatus(record.Status)") {
		t.Fatal("cancellation handler does not return the contracted idempotent status")
	}
}
