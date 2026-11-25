import type { UsageDay } from '#lib/api';

export type DaySpend = { day: string; usd: number };

/**
 * Fill the days with no calls.
 *
 * `byDay` from the server contains only days that have rows, and the panel used
 * to place those bars side by side — so a quiet week looked like a busy one, and
 * the sparkline measured sequence rather than time. Every day in the window gets
 * a bar, and a day with no spend is a zero-height one (issue #52).
 *
 * Days are UTC, the same clock the rows are stored with.
 */
export function dailySeries(byDay: UsageDay[], days: number, now: Date = new Date()): DaySpend[] {
	const spent = new Map(byDay.map((entry) => [entry.day, entry.usd]));
	const series: DaySpend[] = [];

	const end = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
	for (let offset = days - 1; offset >= 0; offset -= 1) {
		const day = new Date(end - offset * 86_400_000).toISOString().slice(0, 10);
		series.push({ day, usd: spent.get(day) ?? 0 });
	}
	return series;
}
