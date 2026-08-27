package httpapi

import (
	"testing"
	"time"
)

func TestWorkerObservationMaximumAge(t *testing.T) {
	if observed := workerObservationMaximumAge(10*time.Second, 60*time.Second); observed != 30*time.Second {
		t.Fatalf("observation window = %s, want 30s", observed)
	}
	if observed := workerObservationMaximumAge(30*time.Second, 60*time.Second); observed != 60*time.Second {
		t.Fatalf("observation window = %s, want lease duration", observed)
	}
}
