import { beforeEach, describe, expect, it } from 'vitest';
import { DOOR_LIMIT_PERIOD, doorLimit, doorLimitKey } from '../src/limits';
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
		const now = 1_800_000_000_000;
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

	it('keys by door and caller, so one noisy source cannot starve another', () => {
		const req = (ip: string) => new Request('https://harbor.test/save', { headers: { 'cf-connecting-ip': ip } });
		expect(doorLimitKey('shortcut', req('1.2.3.4'))).not.toBe(doorLimitKey('telegram', req('1.2.3.4')));
		expect(doorLimitKey('shortcut', req('1.2.3.4'))).not.toBe(doorLimitKey('shortcut', req('5.6.7.8')));
	});

	it('answers 429 with Retry-After once the dampener trips', async () => {
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
