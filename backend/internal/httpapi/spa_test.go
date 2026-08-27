package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSPAHandlerServesAssetsAndHistoryFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("platform index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.js"), []byte("platform asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := spaHandler(root)
	for path, expected := range map[string]string{"/app.js": "platform asset", "/history/run-1": "platform index"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != expected {
			t.Fatalf("unexpected SPA response for %s: status=%d body=%q", path, response.Code, response.Body.String())
		}
	}
}
