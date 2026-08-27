package recovery

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeLeaseRepository struct {
	mu       sync.Mutex
	calls    int
	observed chan struct{}
}

func (repository *fakeLeaseRepository) RecoverExpiredLeases(context.Context) (int64, error) {
	repository.mu.Lock()
	repository.calls++
	repository.mu.Unlock()
	select {
	case repository.observed <- struct{}{}:
	default:
	}
	return 1, nil
}

func TestRunnerRecoversPeriodicallyAndStopsWithContext(t *testing.T) {
	repository := &fakeLeaseRepository{observed: make(chan struct{}, 1)}
	runner, err := New(repository, 5*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()
	select {
	case <-repository.observed:
	case <-time.After(time.Second):
		t.Fatal("recovery loop did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery loop did not stop after cancellation")
	}
}

func TestNewRejectsUnboundedRecoveryLoop(t *testing.T) {
	if _, err := New(&fakeLeaseRepository{}, 0, nil); err == nil {
		t.Fatal("zero interval was accepted")
	}
}
