import fc from 'fast-check';
import { beforeEach, describe, expect, it } from 'vitest';
import { validate, type SchemaName } from '../src/validate';
import { call, resetAll, seedJob, seedTokens, TOKENS } from './helpers';

beforeEach(async () => {
	await resetAll();
	await seedTokens();
});

describe('properties', () => {
	it('validation is total: any JSON either validates or returns typed errors', () => {
		fc.assert(
			fc.property(fc.jsonValue(), (value) => {
				for (const schema of ['save', 'upload', 'job'] as SchemaName[]) {
					const result = validate(schema, value);
					expect(typeof result.valid).toBe('boolean');
					if (!result.valid) {
						expect(result.errors.length).toBeGreaterThan(0);
						for (const error of result.errors) {
							expect(typeof error.keyword).toBe('string');
							expect(typeof error.message).toBe('string');
						}
					}
				}
				return true;
			}),
			{ numRuns: 150 },
		);
	});

	it('never claims one job twice, whatever the load', async () => {
		await fc.assert(
			fc.asyncProperty(
				fc.integer({ min: 1, max: 12 }),
				fc.integer({ min: 1, max: 5 }),
				fc.integer({ min: 1, max: 6 }),
				async (jobCount, limit, clients) => {
					await resetAll();
					await seedTokens();
					for (let i = 0; i < jobCount; i++) {
						await seedJob({ id: `p-${String(i).padStart(2, '0')}`, created_at: 1000 + i });
					}
					const responses = await Promise.all(
						Array.from({ length: clients }, () => call('POST', `/jobs/claim?limit=${limit}`, { token: TOKENS.potato })),
					);
					const ids = responses.flatMap((r) => r.json<{ jobs: { id: string }[] }>().jobs.map((j) => j.id));
					expect(new Set(ids).size).toBe(ids.length);
					expect(ids.length).toBeLessThanOrEqual(jobCount);
					return true;
				},
			),
			{ numRuns: 20 },
		);
	});

	it('a save either lands whole or not at all', async () => {
		await fc.assert(
			fc.asyncProperty(fc.jsonValue(), async (value) => {
				const before = await (await import('cloudflare:test')).env.DB.prepare('SELECT COUNT(*) AS n FROM jobs').first<{ n: number }>();
				const res = await call('POST', '/save', { token: TOKENS.shortcut, body: JSON.stringify(value) });
				const after = await (await import('cloudflare:test')).env.DB.prepare('SELECT COUNT(*) AS n FROM jobs').first<{ n: number }>();
				const delta = (after?.n ?? 0) - (before?.n ?? 0);
				expect(delta).toBe(res.res.status === 200 ? 1 : 0);
				return true;
			}),
			{ numRuns: 40 },
		);
	});
});
