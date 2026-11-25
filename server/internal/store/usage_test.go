package store

import (
	"context"
	"testing"
	"time"
)

// Issue #52: the panel's numbers come from these rows, so the sums, the window
// and the per-item average are the tests that matter.

func record(t *testing.T, s *Store, at time.Time, surface, model, item string, cost float64) {
	t.Helper()
	if err := s.RecordUsage(context.Background(), Usage{
		At:             at,
		Surface:        surface,
		RequestedModel: model,
		ServedModel:    model,
		PromptTokens:   100,
		CostUSD:        cost,
		ItemID:         item,
	}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
}

func TestUsageReportAddsUpAndSplitsBySurface(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	record(t, s, now.Add(-2*time.Hour), "classify", "typesafe/jev-1.14", "item-a", 0.0004)
	record(t, s, now.Add(-2*time.Hour), "write", "deepseek/deepseek-v4.1-flash-1017", "item-a", 0.0010)
	record(t, s, now.Add(-1*time.Hour), "embed", "perplexity/pplx-embed-v1-0.6b", "item-a", 0.0001)
	record(t, s, now.Add(-1*time.Hour), "write", "deepseek/deepseek-v4.1-flash-1017", "item-b", 0.0010)

	report, err := s.UsageReport(ctx, now, 30)
	if err != nil {
		t.Fatalf("UsageReport: %v", err)
	}

	if report.Calls != 4 {
		t.Errorf("Calls = %d, want 4", report.Calls)
	}
	// Float sums, so compare with a tolerance tiny next to a cent.
	if diff := report.TotalUSD - 0.0025; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("TotalUSD = %v, want 0.0025", report.TotalUSD)
	}
	if diff := report.MonthToDateUSD - 0.0025; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("MonthToDateUSD = %v, want 0.0025", report.MonthToDateUSD)
	}
	if report.Month != "2026-11" {
		t.Errorf("Month = %q, want 2026-11", report.Month)
	}

	// The per-surface split has to add up to the total the panel shows.
	var sum float64
	for _, group := range report.BySurface {
		sum += group.USD
	}
	if diff := sum - report.TotalUSD; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("bySurface sums to %v, total is %v", sum, report.TotalUSD)
	}
	if len(report.BySurface) != 3 {
		t.Errorf("bySurface has %d groups, want 3", len(report.BySurface))
	}
	// Most expensive first, which is the order the panel renders.
	if report.BySurface[0].Key != "write" || report.BySurface[0].Calls != 2 {
		t.Errorf("bySurface[0] = %+v, want write with 2 calls", report.BySurface[0])
	}

	if len(report.ByModel) != 3 {
		t.Errorf("byModel has %d groups, want 3", len(report.ByModel))
	}
	if len(report.ByDay) != 1 {
		t.Errorf("byDay has %d days, want 1", len(report.ByDay))
	}
	if report.ByDay[0].Day != "2026-11-24" || report.ByDay[0].Calls != 4 {
		t.Errorf("byDay[0] = %+v, want 2026-11-24 with 4 calls", report.ByDay[0])
	}

	// Two distinct items were enriched, so the per-save average is total/2.
	if report.ItemsWithUsage != 2 {
		t.Errorf("ItemsWithUsage = %d, want 2", report.ItemsWithUsage)
	}
	if diff := report.AvgCostPerItem - 0.00125; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("AvgCostPerItem = %v, want 0.00125", report.AvgCostPerItem)
	}
}

// The calendar month is what lines up with a monthly budget: a call from last
// month is in the total and not in month-to-date.
func TestUsageReportMonthToDateIgnoresLastMonth(t *testing.T) {
	s := openTestStore(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	record(t, s, time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC), "write", "m", "old", 0.01)
	record(t, s, time.Date(2026, 11, 1, 0, 1, 0, 0, time.UTC), "write", "m", "new", 0.02)

	report, err := s.UsageReport(context.Background(), now, 30)
	if err != nil {
		t.Fatalf("UsageReport: %v", err)
	}
	if diff := report.MonthToDateUSD - 0.02; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("MonthToDateUSD = %v, want 0.02", report.MonthToDateUSD)
	}
	if diff := report.TotalUSD - 0.03; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("TotalUSD = %v, want 0.03", report.TotalUSD)
	}
	// The day series is the trailing window, not the calendar month, so both
	// days are there and the October one is what makes them distinguishable.
	if len(report.ByDay) != 2 {
		t.Fatalf("byDay has %d days, want 2: the window is 30 days, not the month", len(report.ByDay))
	}
	if report.ByDay[0].Day != "2026-10-31" || report.ByDay[1].Day != "2026-11-01" {
		t.Errorf("byDay = %+v, want ascending days", report.ByDay)
	}
}

// An unmigrated box answers zeroes with no items, which is what lets the panel
// say "no calls recorded yet" instead of a confident $0.00.
func TestUsageReportOnAnEmptyStore(t *testing.T) {
	report, err := openTestStore(t).UsageReport(context.Background(), time.Now(), 30)
	if err != nil {
		t.Fatalf("UsageReport: %v", err)
	}
	if report.Calls != 0 || report.TotalUSD != 0 || report.AvgCostPerItem != 0 || report.ItemsWithUsage != 0 {
		t.Errorf("report = %+v, want zeroes", report)
	}
	if len(report.BySurface) != 0 || len(report.ByDay) != 0 {
		t.Errorf("report has groups for an empty table: %+v", report)
	}
}
