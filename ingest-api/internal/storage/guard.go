package storage

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

const (
	refreshInterval = 30 * time.Second
	refreshTimeout  = 10 * time.Second
)

type Usage struct {
	UsedBytes   uint64
	BudgetBytes uint64
}

func (usage Usage) OverBudget() bool { return usage.UsedBytes > usage.BudgetBytes }

func (usage Usage) NearBudget() bool { return usage.UsedBytes*10 > usage.BudgetBytes*9 }

type diskMeasurer interface {
	DiskUsedBytes(ctx context.Context) (uint64, error)
}

type Guard struct {
	measurer    diskMeasurer
	budgetBytes uint64
	usedBytes   atomic.Uint64
}

func NewGuard(measurer diskMeasurer, budgetBytes uint64) *Guard {
	return &Guard{measurer: measurer, budgetBytes: budgetBytes}
}

func (guard *Guard) Refresh(ctx context.Context, logger *slog.Logger) error {
	usedBytes, err := guard.measurer.DiskUsedBytes(ctx)
	if err != nil {
		return err
	}
	guard.usedBytes.Store(usedBytes)
	usage := guard.Usage()
	if usage.OverBudget() {
		logger.Error("clickhouse disk budget exceeded, rejecting inserts", "used_bytes", usage.UsedBytes, "budget_bytes", usage.BudgetBytes, "hint", "raise CLICKHOUSE_DISK_BUDGET_BYTES or wait for TTL")
	}
	return nil
}

func (guard *Guard) RunRefreshLoop(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshContext, cancel := context.WithTimeout(ctx, refreshTimeout)
			if err := guard.Refresh(refreshContext, logger); err != nil {
				logger.Warn("measuring clickhouse disk usage failed", "error", err)
			}
			cancel()
		}
	}
}

func (guard *Guard) Usage() Usage {
	return Usage{UsedBytes: guard.usedBytes.Load(), BudgetBytes: guard.budgetBytes}
}
