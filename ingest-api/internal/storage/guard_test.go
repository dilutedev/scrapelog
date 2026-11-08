package storage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

type fakeMeasurer struct {
	usedBytes uint64
	err       error
}

func (measurer *fakeMeasurer) DiskUsedBytes(ctx context.Context) (uint64, error) {
	return measurer.usedBytes, measurer.err
}

func TestUsageBudgetBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		usedBytes uint64
		wantOver  bool
		wantNear  bool
	}{
		{"below ninety percent", 899, false, false},
		{"exactly ninety percent", 900, false, false},
		{"just above ninety percent", 901, false, true},
		{"exactly budget", 1000, false, true},
		{"just above budget", 1001, true, true},
	}
	for _, testCase := range cases {
		usage := Usage{UsedBytes: testCase.usedBytes, BudgetBytes: 1000}
		if usage.OverBudget() != testCase.wantOver || usage.NearBudget() != testCase.wantNear {
			t.Errorf("%s: over=%v near=%v, want over=%v near=%v", testCase.name, usage.OverBudget(), usage.NearBudget(), testCase.wantOver, testCase.wantNear)
		}
	}
}

func TestRefreshStoresMeasurementAndKeepsItOnFailure(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	measurer := &fakeMeasurer{usedBytes: 42}
	guard := NewGuard(measurer, 1000)

	if err := guard.Refresh(context.Background(), logger); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage := guard.Usage(); usage.UsedBytes != 42 || usage.BudgetBytes != 1000 {
		t.Fatalf("usage %+v", usage)
	}

	measurer.err = errors.New("clickhouse down")
	measurer.usedBytes = 7
	if err := guard.Refresh(context.Background(), logger); err == nil {
		t.Fatal("expected error")
	}
	if usage := guard.Usage(); usage.UsedBytes != 42 {
		t.Fatalf("usage changed after failed refresh: %+v", usage)
	}
}
