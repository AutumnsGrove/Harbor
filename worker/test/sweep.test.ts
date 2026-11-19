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

	it('runs from the daily cron without crashing, and the five-minute cron does not sweep', async () => {
		await env.BUCKET.put('incoming/orphan-2/0', new Uint8Array([1]));
		const worker = (await import('../src/index')).default;
		const envs = env as unknown as Cloudflare.Env;
		await worker.scheduled({ cron: '*/5 * * * *', scheduledTime: Date.now(), noRetry() {} } as ScheduledController, envs);
		expect(await env.BUCKET.head('incoming/orphan-2/0')).not.toBeNull();
	});
});
