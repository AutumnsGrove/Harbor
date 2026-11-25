// What the models cost. The pipeline writes one row per billed call; Settings
// reads the aggregates in one round trip (issue #52, plans/03).
//
// The rules that shape this file:
//
//   - Record on the call, not on the item's success. A call that answered and
//     was then rejected was still billed; one that never reached the provider
//     was not.
//   - Store what the provider reported. Never estimate, never re-derive from
//     token counts.
//   - A failed insert is logged by the caller and never fails an enrichment.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Usage is one billed call. It is cost metadata only: the model's answer lives
// in the item, not here.
type Usage struct {
	At               time.Time
	Surface          string // classify | write | embed
	RequestedModel   string
	ServedModel      string
	PromptTokens     int
	CompletionTokens int
	CostUSD          float64
	ItemID           string // "" when the call had no item (a score run, a probe)
}

// RecordUsage writes one row. The caller treats an error as a warning.
func (s *Store) RecordUsage(ctx context.Context, u Usage) error {
	item := sql.NullString{String: u.ItemID, Valid: u.ItemID != ""}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_calls (at, surface, requested_model, served_model,
			prompt_tokens, completion_tokens, cost_usd, item_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.At.UTC(), u.Surface, u.RequestedModel, u.ServedModel,
		u.PromptTokens, u.CompletionTokens, u.CostUSD, item)
	if err != nil {
		return fmt.Errorf("usage: recording a %s call: %w", u.Surface, err)
	}
	return nil
}

// UsageGroup is one row of the per-surface or per-model split.
type UsageGroup struct {
	Key              string  `json:"key"`
	Calls            int     `json:"calls"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	USD              float64 `json:"usd"`
}

// UsageDay is one bar of the sparkline.
type UsageDay struct {
	Day   string  `json:"day"` // YYYY-MM-DD, UTC
	Calls int     `json:"calls"`
	USD   float64 `json:"usd"`
}

// UsageReport is everything the panel needs, from one pass over the table.
type UsageReport struct {
	Month          string  `json:"month"` // YYYY-MM, the calendar month of `now`
	MonthToDateUSD float64 `json:"monthToDateUsd"`
	TotalUSD       float64 `json:"totalUsd"`
	Calls          int     `json:"calls"`

	BySurface []UsageGroup `json:"bySurface"`
	ByModel   []UsageGroup `json:"byModel"`
	ByDay     []UsageDay   `json:"byDay"`

	// AvgCostPerItem divides the all-time spend by the number of distinct items
	// that were ever enriched, which is what "about $X per save" means. Zero
	// when nothing has been recorded.
	AvgCostPerItem float64 `json:"avgCostPerItem"`
	ItemsWithUsage int     `json:"itemsWithUsage"`

	// Days is the window the per-day series covers, echoed so the client does
	// not have to remember what it asked for.
	Days int `json:"days"`
}

// UsageReport aggregates the window ending at now. It is one scan over the `at`
// index plus one small grouped pass, so a year of rows is still milliseconds.
func (s *Store) UsageReport(ctx context.Context, now time.Time, days int) (UsageReport, error) {
	if days < 1 {
		days = 30
	}
	now = now.UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	since := now.AddDate(0, 0, -days)

	report := UsageReport{
		Month:     now.Format("2006-01"),
		Days:      days,
		BySurface: []UsageGroup{},
		ByModel:   []UsageGroup{},
		ByDay:     []UsageDay{},
	}

	// All-time and month-to-date in one row: two conditional sums, one scan.
	err := s.db.QueryRowContext(ctx, `
		SELECT
			coalesce(sum(cost_usd), 0),
			coalesce(sum(CASE WHEN at >= ? THEN cost_usd ELSE 0 END), 0),
			count(*),
			count(DISTINCT item_id)
		FROM usage_calls`, monthStart).Scan(
		&report.TotalUSD, &report.MonthToDateUSD, &report.Calls, &report.ItemsWithUsage)
	if err != nil {
		return UsageReport{}, fmt.Errorf("usage: totals: %w", err)
	}
	if report.ItemsWithUsage > 0 {
		report.AvgCostPerItem = report.TotalUSD / float64(report.ItemsWithUsage)
	}

	if report.BySurface, err = s.usageGroups(ctx, "surface"); err != nil {
		return UsageReport{}, err
	}
	if report.ByModel, err = s.usageGroups(ctx, "requested_model"); err != nil {
		return UsageReport{}, err
	}
	if report.ByDay, err = s.usageDays(ctx, since); err != nil {
		return UsageReport{}, err
	}
	return report, nil
}

// usageGroups groups by a column name chosen here, never by input: the two
// callers are surface and requested_model.
func (s *Store) usageGroups(ctx context.Context, column string) ([]UsageGroup, error) {
	if column != "surface" && column != "requested_model" {
		return nil, errors.New("usage: unexpected grouping column")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+column+` AS key,
			count(*), coalesce(sum(prompt_tokens), 0), coalesce(sum(completion_tokens), 0),
			coalesce(sum(cost_usd), 0)
		FROM usage_calls
		GROUP BY key
		ORDER BY sum(cost_usd) DESC, key`)
	if err != nil {
		return nil, fmt.Errorf("usage: by %s: %w", column, err)
	}
	defer func() { _ = rows.Close() }()

	groups := []UsageGroup{}
	for rows.Next() {
		var group UsageGroup
		if err := rows.Scan(&group.Key, &group.Calls, &group.PromptTokens, &group.CompletionTokens, &group.USD); err != nil {
			return nil, fmt.Errorf("usage: reading by %s: %w", column, err)
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// usageDays is the window only, so the sparkline is the recent shape rather
// than a year of near-empty days.
func (s *Store) usageDays(ctx context.Context, since time.Time) ([]UsageDay, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT substr(at, 1, 10) AS day, count(*), coalesce(sum(cost_usd), 0)
		FROM usage_calls
		WHERE at >= ?
		GROUP BY day
		ORDER BY day`, since)
	if err != nil {
		return nil, fmt.Errorf("usage: by day: %w", err)
	}
	defer func() { _ = rows.Close() }()

	days := []UsageDay{}
	for rows.Next() {
		var day UsageDay
		if err := rows.Scan(&day.Day, &day.Calls, &day.USD); err != nil {
			return nil, fmt.Errorf("usage: reading by day: %w", err)
		}
		days = append(days, day)
	}
	return days, rows.Err()
}
