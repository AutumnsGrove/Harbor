import { beforeEach, describe, expect, it } from 'vitest';
import { reclaimLeases } from '../src/queue';
import { call, env, jobRow, resetAll, seedJob, seedTokens, TOKENS } from './helpers';

beforeEach(async () => {
	await resetAll();
	await seedTokens();
});

const secs = () => Math.floor(Date.now() / 1000);

describe('lease reclaim', () => {
	it('requeues an expired claim and keeps the attempt count', async () => {
		const id = await seedJob({ status: 'claimed', attempts: 1, claimed_at: secs() - 700, lease_until: secs() - 100 });
		const result = await reclaimLeases(env, secs());
		expect(result).toEqual({ requeued: 1, failed: 0 });
		const row = await jobRow(id);
		expect(row?.status).toBe('queued');
		expect(row?.attempts).toBe(1);
		expect(row?.lease_until).toBeNull();
	});

	it('leaves a claim inside its lease alone', async () => {
		await seedJob({ status: 'claimed', attempts: 1, lease_until: secs() + 300 });
		expect(await reclaimLeases(env, secs())).toEqual({ requeued: 0, failed: 0 });
	});

	it('gives up after the attempt budget and says why', async () => {
		const id = await seedJob({ status: 'claimed', attempts: 4, lease_until: secs() - 1 });
		expect(await reclaimLeases(env, secs())).toEqual({ requeued: 0, failed: 1 });
		const row = await jobRow(id);
		expect(row?.status).toBe('failed');
		expect(String(row?.last_error)).toContain('4 attempts');
	});

	it('makes a reclaimed job claimable again with a fresh lease', async () => {
		await seedJob({ status: 'claimed', attempts: 1, lease_until: secs() - 1 });
		await reclaimLeases(env, secs());
		const res = await call('POST', '/jobs/claim?limit=1', { token: TOKENS.potato });
		const jobs = res.json<{ jobs: { attempts: number; status: string }[] }>().jobs;
		expect(jobs.length).toBe(1);
		expect(jobs[0]?.attempts).toBe(2);
		expect(jobs[0]?.status).toBe('claimed');
	});

	it('does not serve a failed job', async () => {
		await seedJob({ status: 'failed', attempts: 5 });
		expect((await call('POST', '/jobs/claim', { token: TOKENS.potato })).json<{ jobs: unknown[] }>().jobs).toEqual([]);
	});

	it('ignores queued and done rows entirely', async () => {
		await seedJob({ status: 'queued' });
		await seedJob({ status: 'done', done_at: secs() });
		expect(await reclaimLeases(env, secs())).toEqual({ requeued: 0, failed: 0 });
	});
});
