import { beforeEach, describe, expect, it } from 'vitest';
import { sweepOrphans } from '../src/queue';
import { call, env, jobRow, resetAll, seedJob, seedTokens, TOKENS } from './helpers';

beforeEach(async () => {
	await resetAll();
	await seedTokens();
});

const secs = () => Math.floor(Date.now() / 1000);
const OLD = -1; // olderThan = -1 makes every existing object older than the cutoff

describe('orphan sweep', () => {
	it('deletes an incoming object with no job row', async () => {
		await env.BUCKET.put('incoming/orphan-1/0', new Uint8Array([1, 2, 3]));
		const result = await sweepOrphans(env, secs(), OLD);
		expect(result.objectsDeleted).toBe(1);
		expect(await env.BUCKET.head('incoming/orphan-1/0')).toBeNull();
	});

	it('keeps an object a live job points at', async () => {
		const id = await seedJob({ withBlob: true });
		await env.BUCKET.put(`incoming/${id}/0`, new Uint8Array([1, 2, 3]));
		const result = await sweepOrphans(env, secs(), OLD);
		expect(result.objectsDeleted).toBe(0);
		expect(await env.BUCKET.head(`incoming/${id}/0`)).not.toBeNull();
	});

	it('never deletes a done job or its blob: that is the replay record', async () => {
		const id = await seedJob({ status: 'done', done_at: secs(), withBlob: true, created_at: secs() - 1000 });
		await env.BUCKET.put(`incoming/${id}/0`, new Uint8Array([1, 2, 3]));
		const result = await sweepOrphans(env, secs(), OLD);
		expect(result.objectsDeleted).toBe(0);
		expect((await jobRow(id))?.status).toBe('done');
		expect(await env.BUCKET.head(`incoming/${id}/0`)).not.toBeNull();
	});

	it('fails a job whose blob is gone instead of deleting it', async () => {
		const id = await seedJob({ status: 'queued', withBlob: true, created_at: secs() - 1000 });
		const result = await sweepOrphans(env, secs(), 100);
		expect(result.jobsFailed).toBe(1);
		const row = await jobRow(id);
		expect(row?.status).toBe('failed');
		expect(row?.last_error).toBe('blob missing from R2');
	});

	it('leaves a young job alone even if its blob is missing', async () => {
		const id = await seedJob({ status: 'queued', withBlob: true });
		expect((await sweepOrphans(env, secs(), 7 * 24 * 3600)).jobsFailed).toBe(0);
		expect((await jobRow(id))?.status).toBe('queued');
	});

	it('the five-minute cron only reclaims: it never sweeps', async () => {
		await env.BUCKET.put('incoming/orphan-2/0', new Uint8Array([1]));
		const worker = (await import('../src/index')).default;
		await worker.scheduled(
			{ cron: '*/5 * * * *', scheduledTime: Date.now(), noRetry() {} } as ScheduledController,
			env as unknown as Cloudflare.Env,
		);
		expect(await env.BUCKET.head('incoming/orphan-2/0')).not.toBeNull();
	});

	it('the daily cron sweeps orphans and prunes old dampener windows', async () => {
		const worker = (await import('../src/index')).default;
		const nowSec = Math.floor(Date.now() / 1000);

		// An orphan object, old enough for a one-second window to catch it. The
		// comparison is whole-second granular (now is floored), so wait two.
		await env.BUCKET.put('incoming/cron-orphan/0', new Uint8Array([1]));
		await new Promise((resolve) => setTimeout(resolve, 2500));

		// A stale dampener window, and a live job whose blob never made it to R2.
		await env.DB.prepare('INSERT INTO door_limits (key, window, hits, updated_at) VALUES (?, ?, 1, ?)')
			.bind('stale', Math.floor(nowSec / 60) - 24 * 60 - 5, nowSec)
			.run();
		const missing = await seedJob({
			status: 'queued',
			withBlob: true,
			created_at: nowSec - 8 * 24 * 3600,
		});

		// The suite's bindings set ORPHAN_AGE_SECONDS=1 so this exercises the real
		// construction path rather than a spread copy of the env.
		await worker.scheduled(
			{ cron: '17 3 * * *', scheduledTime: Date.now(), noRetry() {} } as ScheduledController,
			env as unknown as Cloudflare.Env,
		);

		expect(await env.BUCKET.head('incoming/cron-orphan/0')).toBeNull();
		const limits = await env.DB.prepare("SELECT key FROM door_limits WHERE key = 'stale'").first();
		expect(limits).toBeNull();
		expect((await jobRow(missing))?.status).toBe('failed');
	});
});
