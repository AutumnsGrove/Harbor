import { beforeEach, describe, expect, it } from 'vitest';
import { DOOR_LIMIT_PERIOD, doorLimit, doorLimitKey, pruneDoorLimits } from '../src/limits';
import type { Env } from '../src/types';
import { call, env, resetAll, seedTokens, TOKENS } from './helpers';

beforeEach(async () => {
	await resetAll();
	await seedTokens();
});

// A unique key per test: the counter lives in the colo cache, which outlives a test.
const key = (suffix: string) => `shortcut:test-${suffix}-${Math.random().toString(36).slice(2)}`;
const e = env as unknown as Env;

describe('door dampening', () => {
	it('allows up to the limit in a window, then refuses with a retry hint', async () => {
		const k = key('window');
		// The real clock: a hard-coded future timestamp would leave a future window
		// row behind that the prune test below would later count.
		const now = Date.now();
		for (let i = 0; i < 3; i++) {
			expect((await doorLimit(e, k, { now, limit: 3 })).allowed).toBe(true);
		}
		const blocked = await doorLimit(e, k, { now, limit: 3 });
		expect(blocked.allowed).toBe(false);
		expect(blocked.retryAfter).toBeGreaterThan(0);
		expect(blocked.retryAfter).toBeLessThanOrEqual(DOOR_LIMIT_PERIOD);

		// The next window starts clean.
		expect((await doorLimit(e, k, { now: now + DOOR_LIMIT_PERIOD * 1000, limit: 3 })).allowed).toBe(true);
	});

	it('prunes windows older than a day and keeps the current one', async () => {
		// Start from an empty table so the count is the prune's doing, not leftovers.
		await env.DB.prepare('DELETE FROM door_limits').run();
		const nowSec = Math.floor(Date.now() / 1000);
		const current = Math.floor(nowSec / DOOR_LIMIT_PERIOD);
		const stale = current - 24 * 60 - 10;
		await env.DB.prepare('INSERT INTO door_limits (key, window, hits, updated_at) VALUES (?, ?, 1, ?)')
			.bind('stale', stale, nowSec)
			.run();
		await env.DB.prepare('INSERT INTO door_limits (key, window, hits, updated_at) VALUES (?, ?, 1, ?)')
			.bind('live', current, nowSec)
			.run();

		expect(await pruneDoorLimits(e, nowSec)).toBe(1);
		const rows = await env.DB.prepare('SELECT key FROM door_limits ORDER BY key').all<{ key: string }>();
		const keys = (rows.results ?? []).map((r) => r.key);
		expect(keys).toContain('live');
		expect(keys).not.toContain('stale');
	});

	it('keys by door and caller, so one noisy source cannot starve another', () => {
		const req = (ip: string) => new Request('https://harbor.test/save', { headers: { 'cf-connecting-ip': ip } });
		expect(doorLimitKey('shortcut', req('1.2.3.4'))).not.toBe(doorLimitKey('telegram', req('1.2.3.4')));
		expect(doorLimitKey('shortcut', req('1.2.3.4'))).not.toBe(doorLimitKey('shortcut', req('5.6.7.8')));
	});

	it('answers 429 with Retry-After once the dampener trips', async () => {
		// The counter is keyed by a fixed 60-second window, so a loop that
		// straddles a boundary sees it reset and lets a third request through —
		// which is how this failed once in CI with [200, 200, 200, 429]. Start
		// with a second of room in the window; the wait is under a second and the
		// assertion stays as strict.
		while (Date.now() % (DOOR_LIMIT_PERIOD * 1000) > DOOR_LIMIT_PERIOD * 1000 - 1000) {
			await new Promise((resolve) => setTimeout(resolve, 10));
		}
		// The suite's binding sets DOOR_LIMIT_MAX high; override it for one request.
		const limited = { ...e, DOOR_LIMIT_MAX: '2' } as Env;
		const ip = `203.0.113.${Math.floor(Math.random() * 200)}`;
		const statuses: number[] = [];
		for (let i = 0; i < 4; i++) {
			const res = await call('POST', '/save', {
				token: TOKENS.shortcut,
				body: '{"link":"https://example.com/limited"}',
				headers: { 'cf-connecting-ip': ip },
				env: limited,
			});
			statuses.push(res.res.status);
			if (res.res.status === 429) {
				expect(Number(res.res.headers.get('retry-after'))).toBeGreaterThan(0);
			}
		}
		expect(statuses.slice(0, 2)).toEqual([200, 200]);
		expect(statuses.slice(2)).toEqual([429, 429]);
	});
});
