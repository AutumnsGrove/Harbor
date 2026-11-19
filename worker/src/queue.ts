import { ALBUM_HOLD_SECONDS, LEASE_SECONDS, MAX_ATTEMPTS, ORPHAN_AGE_SECONDS } from './constants';
import type { BlobRow, Env, JobEnvelope, JobRow, Kind, Source } from './types';

// All the SQL lives here. The rules it implements:
//
// - claim is ONE statement (S2 proved it atomic under concurrency). Anything
//   written after it must tolerate failure, because the claim has already
//   committed by then.
// - done jobs and their blobs are never deleted, so `/jobs/replay` can refill a
//   rollback window after a restore (design.md §4, §11).
// - the album hold is `touched_at`, never `updated_at`: claim and lease refresh
//   must not make an album look freshly touched.

const JOB_COLUMNS = `id, source, kind, link, note, title, payload_json, group_key,
	touched_at, status, attempts, claimed_at, lease_until, last_error,
	created_at, updated_at, done_at`;

function envelope(job: JobRow, blobs: BlobRow[]): JobEnvelope {
	return {
		id: job.id,
		source: job.source,
		kind: job.kind,
		link: job.link,
		note: job.note,
		title: job.title,
		group_key: job.group_key,
		created_at: job.created_at,
		status: job.status,
		attempts: job.attempts,
		last_error: job.last_error,
		blobs: blobs
			.filter((b) => b.job_id === job.id)
			.sort((a, b) => a.n - b.n)
			.map((b) => ({ n: b.n, key: b.r2_key, content_type: b.content_type, bytes: b.bytes })),
	};
}

async function attachBlobs(env: Env, jobs: JobRow[]): Promise<JobEnvelope[]> {
	if (jobs.length === 0) return [];
	const marks = jobs.map(() => '?').join(',');
	const { results } = await env.DB.prepare(
		`SELECT job_id, n, r2_key, content_type, bytes, created_at FROM blobs WHERE job_id IN (${marks}) ORDER BY n`,
	)
		.bind(...jobs.map((j) => j.id))
		.all<BlobRow>();
	return jobs.map((j) => envelope(j, results ?? []));
}

// ── enqueue (door side) ─────────────────────────────────────────────────────

export async function enqueueLink(
	env: Env,
	now: number,
	args: { source: Source; link: string | null; note: string | null; title: string | null },
): Promise<string> {
	const id = crypto.randomUUID();
	const kind: Kind = args.link ? 'link' : 'note';
	await env.DB.prepare(
		`INSERT INTO jobs (id, source, kind, link, note, title, touched_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
		.bind(id, args.source, kind, args.link, args.note, args.title, now, now, now)
		.run();
	return id;
}

export interface UploadRef {
	id: string;
	key: string;
}

/** Mint the job id and R2 key *before* the body is written: the row must not exist first. */
export function newUploadRef(): UploadRef {
	const id = crypto.randomUUID();
	return { id, key: incomingKey(id, 0) };
}

/**
 * Record an upload whose object is already in R2. R2 first, D1 second: the
 * reverse order would let a claim see a job whose blob is not there yet. If this
 * insert fails, the object is an orphan and the daily sweep takes it.
 */
export async function insertUpload(
	env: Env,
	now: number,
	ref: UploadRef,
	args: {
		source: Source;
		contentType: string;
		bytes: number;
		link: string | null;
		note: string | null;
		title: string | null;
		filename: string | null;
	},
): Promise<string> {
	// The filename is not a column: it belongs to the door's payload, and the
	// potato names the downloaded file from the content type and its own rules.
	const payload = JSON.stringify(args.filename ? { filename: args.filename } : {});
	await env.DB.batch([
		env.DB.prepare(
			`INSERT INTO jobs (id, source, kind, link, note, title, payload_json, touched_at, created_at, updated_at)
			 VALUES (?, ?, 'upload', ?, ?, ?, ?, ?, ?, ?)`,
		).bind(ref.id, args.source, args.link, args.note, args.title, payload, now, now, now),
		env.DB.prepare(
			`INSERT INTO blobs (job_id, n, r2_key, content_type, bytes, created_at) VALUES (?, 0, ?, ?, ?, ?)`,
		).bind(ref.id, ref.key, args.contentType, args.bytes, now),
	]);
	return ref.id;
}

export function incomingKey(jobId: string, n: number): string {
	return `incoming/${jobId}/${n}`;
}

// ── claim (potato side) ─────────────────────────────────────────────────────

/**
 * Claim up to `limit` jobs in one statement. `kinds` lets the potato ask only for
 * what it can process yet, so a photo waits safely before phase 6 instead of
 * burning attempts.
 */
export async function claim(
	env: Env,
	now: number,
	args: { limit: number; kinds: Kind[] },
): Promise<JobEnvelope[]> {
	const kinds = args.kinds.length > 0 ? args.kinds : (['link', 'note', 'upload'] as Kind[]);
	const marks = kinds.map(() => '?').join(',');
	const { results } = await env.DB.prepare(
		`UPDATE jobs
		 SET status = 'claimed', attempts = attempts + 1, claimed_at = ?, lease_until = ?, updated_at = ?
		 WHERE id IN (
		   SELECT id FROM jobs
		   WHERE status = 'queued'
		     AND (group_key IS NULL OR touched_at < ?)
		     AND kind IN (${marks})
		   ORDER BY created_at
		   LIMIT ?
		 )
		 RETURNING ${JOB_COLUMNS}`,
	)
		.bind(now, now + LEASE_SECONDS, now, now - ALBUM_HOLD_SECONDS, ...kinds, args.limit)
		.all<JobRow>();
	return attachBlobs(env, results ?? []);
}

// ── reading (potato side, read-only) ────────────────────────────────────────

export async function pending(env: Env): Promise<JobEnvelope[]> {
	const { results } = await env.DB.prepare(
		`SELECT ${JOB_COLUMNS} FROM jobs WHERE status IN ('queued', 'claimed') ORDER BY created_at`,
	).all<JobRow>();
	return attachBlobs(env, results ?? []);
}

export async function replay(env: Env, since: number): Promise<JobEnvelope[]> {
	const { results } = await env.DB.prepare(
		`SELECT ${JOB_COLUMNS} FROM jobs WHERE status = 'done' AND done_at >= ? ORDER BY done_at, created_at`,
	)
		.bind(since)
		.all<JobRow>();
	return attachBlobs(env, results ?? []);
}

// ── closing a job (potato side) ─────────────────────────────────────────────

export async function markDone(env: Env, now: number, id: string): Promise<'ok' | 'missing'> {
	const exists = await env.DB.prepare('SELECT id FROM jobs WHERE id = ?').bind(id).first<{ id: string }>();
	if (!exists) return 'missing';
	// Idempotent: a second done is a no-op that keeps the original done_at, so a
	// replay window cannot grow or shrink because of a retried request.
	await env.DB.prepare(
		`UPDATE jobs SET status = 'done', done_at = COALESCE(done_at, ?), updated_at = ?
		 WHERE id = ? AND status != 'done'`,
	)
		.bind(now, now, id)
		.run();
	return 'ok';
}

export async function markFailed(
	env: Env,
	now: number,
	id: string,
	error: string,
): Promise<'ok' | 'missing'> {
	const exists = await env.DB.prepare('SELECT id FROM jobs WHERE id = ?').bind(id).first<{ id: string }>();
	if (!exists) return 'missing';
	await env.DB.prepare(
		`UPDATE jobs SET status = 'failed', last_error = ?, updated_at = ? WHERE id = ? AND status != 'done'`,
	)
		.bind(error.slice(0, 500), now, id)
		.run();
	return 'ok';
}

// ── the two crons ───────────────────────────────────────────────────────────

/** Lease reclaim: a dead potato must not strand a save (design.md §4). */
export async function reclaimLeases(env: Env, now: number): Promise<{ requeued: number; failed: number }> {
	const requeued = await env.DB.prepare(
		`UPDATE jobs SET status = 'queued', lease_until = NULL, updated_at = ?
		 WHERE status = 'claimed' AND lease_until IS NOT NULL AND lease_until < ? AND attempts < ?`,
	)
		.bind(now, now, MAX_ATTEMPTS)
		.run();

	const failed = await env.DB.prepare(
		`UPDATE jobs SET status = 'failed', last_error = 'lease expired after ' || attempts || ' attempts',
		 updated_at = ?
		 WHERE status = 'claimed' AND lease_until IS NOT NULL AND lease_until < ? AND attempts >= ?`,
	)
		.bind(now, now, MAX_ATTEMPTS)
		.run();

	return { requeued: requeued.meta.changes ?? 0, failed: failed.meta.changes ?? 0 };
}

export interface SweepResult {
	objectsDeleted: number;
	jobsFailed: number;
}

/**
 * The daily sweep, both directions (design.md §11):
 *
 * - an `incoming/` object with no blob row is an orphan from a failed D1 insert;
 *   delete it once it is older than the window.
 * - a blob row whose object is gone is a job that cannot ever be processed;
 *   mark it failed rather than deleting anything. The reverse case is data loss.
 *
 * Done jobs and their objects are never touched: they are the replay record.
 */
export async function sweepOrphans(
	env: Env,
	now: number,
	olderThan = ORPHAN_AGE_SECONDS,
): Promise<SweepResult> {
	const cutoff = now - olderThan;

	const referenced = new Set<string>();
	const { results: blobRows } = await env.DB.prepare('SELECT r2_key FROM blobs').all<{ r2_key: string }>();
	for (const row of blobRows ?? []) referenced.add(row.r2_key);

	// List what is actually in the bucket, paged.
	const present = new Set<string>();
	let cursor: string | undefined;
	do {
		const page = await env.BUCKET.list({ prefix: 'incoming/', cursor, limit: 1000 });
		for (const object of page.objects) present.add(object.key);
		cursor = page.truncated ? (page.cursor as string) : undefined;
	} while (cursor);

	let objectsDeleted = 0;
	let cursor2: string | undefined;
	do {
		const page = await env.BUCKET.list({ prefix: 'incoming/', cursor: cursor2, limit: 1000 });
		for (const object of page.objects) {
			const uploaded = object.uploaded instanceof Date ? object.uploaded.getTime() : Number(object.uploaded);
			if (uploaded / 1000 > cutoff) continue;
			if (referenced.has(object.key)) continue;
			await env.BUCKET.delete(object.key);
			objectsDeleted++;
		}
		cursor2 = page.truncated ? (page.cursor as string) : undefined;
	} while (cursor2);

	// A live job whose blob is missing can never succeed. Fail it, loudly.
	const { results: orphans } = await env.DB.prepare(
		`SELECT b.job_id AS job_id, b.r2_key AS r2_key FROM blobs b
		 JOIN jobs j ON j.id = b.job_id
		 WHERE b.created_at < ? AND j.status IN ('queued', 'claimed')`,
	)
		.bind(cutoff)
		.all<{ job_id: string; r2_key: string }>();

	let jobsFailed = 0;
	for (const row of orphans ?? []) {
		if (present.has(row.r2_key)) continue;
		await env.DB.prepare(
			`UPDATE jobs SET status = 'failed', last_error = 'blob missing from R2', updated_at = ?
			 WHERE id = ? AND status IN ('queued', 'claimed')`,
		)
			.bind(now, row.job_id)
			.run();
		jobsFailed++;
	}

	return { objectsDeleted, jobsFailed };
}

/** Does this key belong to a job whose row still exists? Used by /presign. */
export async function knownKey(env: Env, key: string): Promise<boolean> {
	const row = await env.DB.prepare('SELECT 1 AS ok FROM blobs WHERE r2_key = ?').bind(key).first<{ ok: number }>();
	return row !== null;
}
