// Package recovery runs bounded, persistent Worker lease recovery.
package recovery

import (
	"context"
	"fmt"
	"time"
)

type LeaseRepository interface {
	RecoverExpiredLeases(context.Context) (int64, error)
}

// Runner owns no task state. Every recovery attempt delegates to the
// PostgreSQL transaction that is also serialized with Worker claims.
type Runner struct {
	repository LeaseRepository
	interval   time.Duration
	report     func(recovered int64, err error)
}

func New(repository LeaseRepository, interval time.Duration, report func(recovered int64, err error)) (Runner, error) {
	if repository == nil {
		return Runner{}, fmt.Errorf("lease recovery repository is required")
	}
	if interval <= 0 {
		return Runner{}, fmt.Errorf("lease recovery interval must be positive")
	}
	return Runner{repository: repository, interval: interval, report: report}, nil
}

func (runner Runner) RecoverOnce(ctx context.Context) (int64, error) {
	return runner.repository.RecoverExpiredLeases(ctx)
}

// Run performs periodic recovery until its context is cancelled. It returns
// only after an in-flight database call observes that cancellation.
func (runner Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(runner.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			recovered, err := runner.RecoverOnce(ctx)
			if runner.report != nil {
				runner.report(recovered, err)
			}
		}
	}
}
