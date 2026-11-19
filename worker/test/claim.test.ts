import { beforeEach, describe, expect, it } from 'vitest';
import { call, env, resetAll, jobRow, seedJob, seedTokens, TOKENS } from './helpers';

beforeEach(async () => {
	await resetAll();
	await seedTokens();
});

interface Envelope {
	id: string;
	status: string;
	attempts: number;
	touched_at?: number;
	blobs: unknown[];
}

async function claim(query = ''): Promise<Envelope[]> {
	const res = await call('POST', `/jobs/claim${query}`, { token: TOKENS.potato });
	expect(res.res.status, res.text).toBe(200);
	return res.json<{ jobs: Envelope[] }>().jobs;
}

async function seedMany(n: number): Promise<string[]> {
	const ids: string[] = [];
	for (let i = 0; i < n; i++) ids.push(await seedJob({ id: `job-${String(i).padStart(2, '0')}`, created_at: 1000 + i }));
	return ids;
}

describe('claim', () => {
	it('gives one job to one claimer only', async () => {
		const id = await seedJob({});
		const first = await claim('?limit=1');
		expect(first.map((j) => j.id)).toEqual([id]);
		const second = await claim('?limit=1');
		expect(second).toEqual([]);
	});

	it('returns whole rows and their blobs', async () => {
		const id = await seedJob({ kind: 'upload', withBlob: true });
		const [job] = await claim('?limit=1');
		expect(job?.id).toBe(id);
		expect(job?.status).toBe('claimed');
		expect(job?.attempts).toBe(1);
		expect(job?.blobs).toEqual([
			{ n: 0, key: `incoming/${id}/0`, content_type: 'image/jpeg', bytes: 1024 },
		]);
	});

	it('pages through 12 jobs five at a time with no overlap', async () => {
		await seedMany(12);
		const a = await claim('?limit=5');
		const b = await claim('?limit=5');
		const c = await claim('?limit=5');
		expect([a.length, b.length, c.length]).toEqual([5, 5, 2]);
		const all = [...a, ...b, ...c].map((j) => j.id);
		expect(new Set(all).size).toBe(12);
	});

	it('is atomic under concurrency', async () => {
		await seedMany(20);
		const bursts = await Promise.all(Array.from({ length: 20 }, () => claim('?limit=1')));
		const ids = bursts.flat().map((j) => j.id);
		expect(ids.length).toBe(20);
		expect(new Set(ids).size).toBe(20);
	});

	it('honours kinds, so a photo waits instead of burning attempts', async () => {
		await seedJob({ kind: 'upload', withBlob: true });
		const link = await seedJob({ kind: 'link' });
		expect((await claim('?kinds=link')).map((j) => j.id)).toEqual([link]);
		expect(await claim('?kinds=note')).toEqual([]);
		expect((await claim('?kinds=upload')).length).toBe(1);
	});

	it('rejects an unknown kind instead of guessing', async () => {
		const res = await call('POST', '/jobs/claim?kinds=video', { token: TOKENS.potato });
		expect(res.res.status).toBe(400);
	});

	it('clamps a silly limit', async () => {
		await seedMany(3);
		expect((await claim('?limit=1000')).length).toBe(3);
		expect((await claim('?limit=0')).length).toBe(0);
	});
});

describe('album hold', () => {
	it('holds an album touched seconds ago and claims a stale one', async () => {
		const now = Math.floor(Date.now() / 1000);
		const held = await seedJob({ id: 'held', kind: 'upload', group_key: 'chat:1', touched_at: now - 3, withBlob: true });
		const ready = await seedJob({ id: 'ready', kind: 'upload', group_key: 'chat:2', touched_at: now - 20, withBlob: true });
		const plain = await seedJob({ id: 'plain', touched_at: now });

		const claimed = (await claim('?limit=5')).map((j) => j.id);
		expect(claimed).toContain(ready);
		expect(claimed).toContain(plain);
		expect(claimed).not.toContain(held);
	});

	it('never moves touched_at when claiming', async () => {
		const now = Math.floor(Date.now() / 1000);
		const id = await seedJob({ group_key: 'chat:3', touched_at: now - 30, withBlob: true, kind: 'upload' });
		await claim('?limit=1');
		const row = await jobRow(id);
		expect(row?.touched_at).toBe(now - 30);
		expect(row?.status).toBe('claimed');
	});
});

describe('pending', () => {
	it('returns queued and claimed jobs and changes nothing', async () => {
		await seedJob({ id: 'queued-1' });
		await seedJob({ id: 'claimed-1', status: 'claimed', attempts: 1, lease_until: Math.floor(Date.now() / 1000) + 600 });
		await seedJob({ id: 'done-1', status: 'done', done_at: 1234 });

		const before = await env.DB.prepare('SELECT id, status, updated_at, claimed_at FROM jobs ORDER BY id').all();
		const res = await call('GET', '/jobs/pending', { token: TOKENS.potato });
		const after = await env.DB.prepare('SELECT id, status, updated_at, claimed_at FROM jobs ORDER BY id').all();

		expect(res.res.status).toBe(200);
		const jobs = res.json<{ jobs: Envelope[] }>().jobs;
		expect(jobs.map((j) => j.id).sort()).toEqual(['claimed-1', 'queued-1']);
		expect(after.results).toEqual(before.results);
	});
});

describe('closing a job', () => {
	it('is idempotent and keeps the original done_at', async () => {
		const id = await seedJob({});
		const first = await call('POST', `/jobs/${id}/done`, { token: TOKENS.potato });
		const doneAt = (await jobRow(id))?.done_at;
		const second = await call('POST', `/jobs/${id}/done`, { token: TOKENS.potato });
		expect(first.res.status).toBe(200);
		expect(second.res.status).toBe(200);
		expect((await jobRow(id))?.status).toBe('done');
		expect((await jobRow(id))?.done_at).toBe(doneAt);
	});

	it('404s for a job that never existed', async () => {
		expect((await call('POST', '/jobs/nope/done', { token: TOKENS.potato })).res.status).toBe(404);
		expect((await call('POST', '/jobs/nope/failed', { token: TOKENS.potato })).res.status).toBe(404);
	});

	it('stores the failure reason and keeps attempts', async () => {
		const id = await seedJob({});
		await claim('?limit=1');
		const res = await call('POST', `/jobs/${id}/failed`, {
			token: TOKENS.potato,
			body: '{"error":"the page was a PDF"}',
		});
		expect(res.res.status).toBe(200);
		const row = await jobRow(id);
		expect(row?.status).toBe('failed');
		expect(row?.last_error).toBe('the page was a PDF');
		expect(row?.attempts).toBe(1);
	});

	it('does not un-done a done job with a late failure', async () => {
		const id = await seedJob({});
		await call('POST', `/jobs/${id}/done`, { token: TOKENS.potato });
		await call('POST', `/jobs/${id}/failed`, { token: TOKENS.potato, body: '{"error":"late"}' });
		expect((await jobRow(id))?.status).toBe('done');
	});
});

describe('replay', () => {
	it('returns the done jobs since a time, with their blobs', async () => {
		const t = Math.floor(Date.now() / 1000);
		const kept: string[] = [];
		for (let i = 0; i < 10; i++) kept.push(await seedJob({ status: 'done', done_at: t - 10 + i, withBlob: true }));
		await seedJob({ id: 'ancient', status: 'done', done_at: t - 100000 });
		await seedJob({ id: 'queued-one' });

		const res = await call('GET', `/jobs/replay?since=${t - 60}`, { token: TOKENS.potato });
		expect(res.res.status).toBe(200);
		const jobs = res.json<{ jobs: Envelope[] }>().jobs;
		expect(jobs.map((j) => j.id).sort()).toEqual([...kept].sort());
		for (const job of jobs) expect(job.blobs.length).toBe(1);
	});

	it('accepts an ISO timestamp and rejects nonsense', async () => {
		expect((await call('GET', '/jobs/replay?since=2026-11-01T00:00:00Z', { token: TOKENS.potato })).res.status).toBe(200);
		expect((await call('GET', '/jobs/replay?since=yesterday', { token: TOKENS.potato })).res.status).toBe(400);
	});
});
