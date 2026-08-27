package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zx/federated-iot-platform/backend/internal/storage/postgres"
)

func TestRequestIDAcceptsOnlyTrustedReqTokensAndKeepsEnvelopeInSync(t *testing.T) {
	api := &API{}
	handler := api.requestID(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusOK, envelope(request, map[string]any{"ok": true}))
	}))

	valid := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	valid.Header.Set("X-Request-ID", "req_live-01_OK")
	validRecorder := httptest.NewRecorder()
	handler.ServeHTTP(validRecorder, valid)
	if validRecorder.Header().Get("X-Request-ID") != "req_live-01_OK" {
		t.Fatalf("valid request id was not preserved: %q", validRecorder.Header().Get("X-Request-ID"))
	}
	if got := responseMetaRequestID(t, validRecorder); got != "req_live-01_OK" {
		t.Fatalf("valid envelope request id = %q", got)
	}

	for _, supplied := range []string{"", "ui_live", "req space", "request_123", "req_" + strings.Repeat("x", 125)} {
		t.Run("invalid_"+strings.ReplaceAll(supplied, " ", "_"), func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
			if supplied != "" {
				request.Header.Set("X-Request-ID", supplied)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			generated := recorder.Header().Get("X-Request-ID")
			if !trustedRequestID.MatchString(generated) || len(generated) > 128 || generated == supplied {
				t.Fatalf("invalid request id %q produced %q", supplied, generated)
			}
			if got := responseMetaRequestID(t, recorder); got != generated {
				t.Fatalf("envelope request id = %q, header = %q", got, generated)
			}
		})
	}

	multiple := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	multiple.Header.Add("X-Request-ID", "req_first")
	multiple.Header.Add("X-Request-ID", "req_second")
	multipleRecorder := httptest.NewRecorder()
	handler.ServeHTTP(multipleRecorder, multiple)
	if got := multipleRecorder.Header().Get("X-Request-ID"); got == "req_first" || got == "req_second" || !trustedRequestID.MatchString(got) {
		t.Fatalf("multiple request id header values were trusted: %q", got)
	}
}

func TestRequestIDAlsoBindsStableErrorResponse(t *testing.T) {
	api := &API{}
	handler := api.requestID(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		api.writeError(writer, request, postgres.StableError{Code: "REQUEST_INVALID", Message: "Request is invalid.", Recoverable: true})
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	request.Header.Set("X-Request-ID", "ui_invalid")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var response struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.RequestID == "ui_invalid" || response.RequestID != recorder.Header().Get("X-Request-ID") || !trustedRequestID.MatchString(response.RequestID) {
		t.Fatalf("stable error does not carry the generated request id: header=%q body=%q", recorder.Header().Get("X-Request-ID"), response.RequestID)
	}
}

func responseMetaRequestID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response struct {
		Meta struct {
			RequestID string `json:"request_id"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Meta.RequestID
}
