import { describe, expect, it } from 'vitest';

import { dailySeries } from './sparkline';

// #52: the sparkline has to be time, not a sequence of days that happened to
// have calls. The server sends only the days with rows; the panel fills the gap.

const now = new Date('2026-11-24T12:00:00Z');

describe('dailySeries', () => {
	it('gives every day in the window a bar, including the quiet ones', () => {
		const series = dailySeries(
			[
				{ day: '2026-11-20', calls: 3, usd: 0.003 },
				{ day: '2026-11-24', calls: 1, usd: 0.001 }
			],
			5,
			now
		);

		expect(series.map((entry) => entry.day)).toEqual([
			'2026-11-20',
			'2026-11-21',
			'2026-11-22',
			'2026-11-23',
			'2026-11-24'
		]);
		expect(series.map((entry) => entry.usd)).toEqual([0.003, 0, 0, 0, 0.001]);
	});

	it('puts a day with no calls at zero, not at the previous day’s height', () => {
		const series = dailySeries([{ day: '2026-11-23', calls: 2, usd: 0.01 }], 3, now);
		expect(series.at(-1)).toEqual({ day: '2026-11-24', usd: 0 });
	});

	it('ignores rows outside the window and keeps the order oldest first', () => {
		const series = dailySeries(
			[
				{ day: '2026-11-01', calls: 1, usd: 9 },
				{ day: '2026-11-24', calls: 1, usd: 0.5 }
			],
			2,
			now
		);

		expect(series).toEqual([
			{ day: '2026-11-23', usd: 0 },
			{ day: '2026-11-24', usd: 0.5 }
		]);
	});

	it('crosses a month boundary without skipping a day', () => {
		const series = dailySeries([], 3, new Date('2026-12-01T00:30:00Z'));
		expect(series.map((entry) => entry.day)).toEqual(['2026-11-29', '2026-11-30', '2026-12-01']);
	});
});
