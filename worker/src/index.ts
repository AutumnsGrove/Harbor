import { Hono } from 'hono';
import type { Context } from 'hono';
import { authenticate } from './auth';
import {
	BACKUP_LIMIT_BYTES,
	NameError,
	deleteBackup,
	listBackups,
	putBackup,
} from './backups';
import {
	MAX_SAVE_BODY_BYTES,
	MAX_UPLOAD_BYTES,
	contentAllowed,
} from './constants';
import { doorLimit, doorLimitKey, pruneDoorLimits } from './limits';
import { bucketFor, presign, presignConfigured, presignKeyScope } from './presign';
import {
	claim,
	insertUpload,
	knownKey,
	enqueueLink,
	markDone,
	markFailed,
	newUploadRef,
	pending,
	reclaimLeases,
	replay,
	sweepOrphans,
} from './queue';
import type { Env, Kind, Principal, Scope, Source } from './types';
import { validate } from './validate';

// The whole public surface. design.md §4 is the source of truth for this table;
// a route that is not in it does not exist, and one that is here declares a scope
// through `route()` so a later change cannot quietly add an unauthenticated hole.
//
// Three properties this file exists to keep:
//   1. a door token may only append — scope `door` on /save and /upload, nothing else;
//   2. there is no public read path: every potato route needs the potato token;
//   3. nothing here holds or can reconstruct the library.

type RouteScope = Scope | 'none';

const app = new Hono<{ Bindings: Env; Variables: { principal: Principal } }>();

export const ROUTE_SCOPES = new Map<string, RouteScope>();

type Handler = (c: Context<{ Bindings: Env; Variables: { principal: Principal } }>) => Response | Promise<Response>;

function route(method: 'GET' | 'POST' | 'PUT' | 'DELETE', path: string, scope: RouteScope, handler: Handler): void {
	ROUTE_SCOPES.set(`${method} ${path}`, scope);
	if (scope === 'none') {
		app.on(method, path, handler);
		return;
	}
	app.on(method, path, requireScope(scope), handler);
}

// ── identity ────────────────────────────────────────────────────────────────

function requireScope(scope: Scope) {
	return async (c: Context<{ Bindings: Env; Variables: { principal: Principal } }>, next: () => Promise<void>) => {
		const now = Math.floor(Date.now() / 1000);
		const principal = await authenticate(c.req.raw, c.env, now, (p) => c.executionCtx.waitUntil(p));
		if (!principal) {
			// Identical response for absent, wrong and revoked: a probe learns nothing.
			return json({ error: 'unauthorized' }, 401);
		}
		if (principal.scope !== scope) {
			// A door token on a potato route, or the reverse. No side effects, no hints.
			return json({ error: 'forbidden' }, 403);
		}
		c.set('principal', principal);
		await next();
	};
}

// ── helpers ─────────────────────────────────────────────────────────────────


function json(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store', ...headers },
	});
}

function nowSeconds(): number {
	return Math.floor(Date.now() / 1000);
}

/** 429 with a Retry-After, per design.md §11. */
function tooMany(retryAfter: number): Response {
	return json({ error: 'slow down', retry_after: retryAfter }, 429, { 'retry-after': String(retryAfter) });
}

const KINDS: Kind[] = ['link', 'note', 'upload'];
const SOURCES: Source[] = ['telegram', 'shortcut'];

/**
 * The token id *is* the source. A token minted under another name is a
 * misconfiguration, and the failure must be loud here rather than a job the
 * potato rejects later because its `source` is outside the contract's enum.
 */
function doorSource(id: string): Source | null {
	return SOURCES.includes(id as Source) ? (id as Source) : null;
}

function parseKinds(raw: string | undefined): Kind[] | null {
	if (!raw) return [];
	const parts = raw
		.split(',')
		.map((s) => s.trim())
		.filter(Boolean);
	const unknown = parts.filter((p) => !KINDS.includes(p as Kind));
	if (unknown.length > 0) return null;
	return [...new Set(parts)] as Kind[];
}

function parseLimit(raw: string | undefined): number {
	const n = Number(raw ?? 5);
	if (!Number.isFinite(n) || n < 1) return 5;
	return Math.min(Math.floor(n), 50);
}

/**
 * Read a stream with a hard cap. Returns null if the body exceeds it. Only used
 * when a caller supplies no Content-Length (S1: the edge always does).
 */
async function readCapped(stream: ReadableStream<Uint8Array>, cap: number): Promise<Uint8Array | null> {
	const reader = stream.getReader();
	const chunks: Uint8Array[] = [];
	let total = 0;
	for (;;) {
		const { done, value } = await reader.read();
		if (done) break;
		total += value.byteLength;
		if (total > cap) {
			await reader.cancel().catch(() => undefined);
			return null;
		}
		chunks.push(value);
	}
	const out = new Uint8Array(total);
	let offset = 0;
	for (const chunk of chunks) {
		out.set(chunk, offset);
		offset += chunk.byteLength;
	}
	return out;
}

async function readJsonCapped(req: Request): Promise<{ ok: true; value: unknown } | { ok: false; response: Response }> {
	const declared = req.headers.get('content-length');
	if (declared !== null && Number(declared) > MAX_SAVE_BODY_BYTES) {
		return { ok: false, response: json({ error: 'body too large', limit: MAX_SAVE_BODY_BYTES }, 413) };
	}
	const text = await req.text();
	if (text.length > MAX_SAVE_BODY_BYTES) {
		return { ok: false, response: json({ error: 'body too large', limit: MAX_SAVE_BODY_BYTES }, 413) };
	}
	if (text.trim() === '') return { ok: false, response: json({ error: 'empty body' }, 400) };
	try {
		return { ok: true, value: JSON.parse(text) };
	} catch {
		return { ok: false, response: json({ error: 'not json' }, 400) };
	}
}

// ── doors (append only) ─────────────────────────────────────────────────────

route('POST', '/save', 'door', async (c) => {
	const principal = c.get('principal');
	const limit = await doorLimit(c.env, doorLimitKey(principal.id, c.req.raw));
	if (!limit.allowed) return tooMany(limit.retryAfter);

	const body = await readJsonCapped(c.req.raw);
	if ('response' in body) return body.response;

	const result = validate('save', body.value);
	if (!result.valid) return json({ error: 'invalid', errors: result.errors }, 400);

	const source = doorSource(principal.id);
	if (!source) return json({ error: 'unknown door' }, 403);

	const input = body.value as { link?: string; note?: string; title?: string };
	const now = nowSeconds();
	const id = await enqueueLink(c.env, now, {
		source,
		link: input.link ?? null,
		note: input.note ?? null,
		title: input.title ?? null,
	});
	return json({ status: 'queued', id }, 200);
});

route('POST', '/upload', 'door', async (c) => {
	const principal = c.get('principal');
	const limit = await doorLimit(c.env, doorLimitKey(principal.id, c.req.raw));
	if (!limit.allowed) return tooMany(limit.retryAfter);

	const contentType = (c.req.header('content-type') ?? '').split(';')[0]!.trim();
	// Build the object without undefined keys: a validator must see what the wire
	// would carry, and a key holding undefined is neither absent nor present.
	const optional: Record<string, string> = {};
	const filename = c.req.query('filename') ?? c.req.header('x-harbor-filename');
	if (filename) optional.filename = filename;
	for (const key of ['link', 'note', 'title'] as const) {
		const value = c.req.query(key);
		if (value) optional[key] = value;
	}
	const metadata: Record<string, string> = { content_type: contentType, ...optional };
	// The allowlist first: a zip is a 415 with one line, not a 400 with a list of
	// pattern errors pointing at a schema the caller has never read.
	if (!contentAllowed(contentType)) return json({ error: 'content type not allowed', got: contentType }, 415);
	const meta = validate('upload', metadata);
	if (!meta.valid) return json({ error: 'invalid', errors: meta.errors }, 400);

	// S1: the edge hands the Worker a length even for a chunked body, so this is
	// a guarantee, not a best effort. The counting stream below is defence in
	// depth for a runtime that ever stops doing that - and it is what makes a
	// body that lies about its length fail instead of landing half-written.
	// Hono's header() returns undefined for a missing header, not null: `!== null`
	// would then read "undefined" as a length. Normalise before comparing.
	const declared = c.req.header('content-length') ?? null;
	if (declared !== null) {
		const bytes = Number(declared);
		if (!Number.isFinite(bytes) || bytes <= 0) return json({ error: 'no body' }, 400);
		if (bytes > MAX_UPLOAD_BYTES) {
			return json({ error: 'file too large', limit: MAX_UPLOAD_BYTES, got: bytes }, 413);
		}
	}
	const incoming = c.req.raw.body;
	if (!incoming) return json({ error: 'no body' }, 400);

	const source = doorSource(principal.id);
	if (!source) return json({ error: 'unknown door' }, 403);

	const ref = newUploadRef();
	let object: R2Object | null;
	try {
		if (declared !== null) {
			// A request body carries a known length, which is what R2 wants: this is
			// the production path, and S1 proved it streams a 25 MB body in ~2 s.
			object = await c.env.BUCKET.put(ref.key, incoming, {
				httpMetadata: { contentType },
				customMetadata: { source, job: ref.id },
			});
		} else {
			// No declared length: only reachable from a direct in-process call, since
			// the edge always supplies one. Buffer with a hard cap rather than feed R2
			// a stream whose length it cannot verify.
			const buffered = await readCapped(incoming, MAX_UPLOAD_BYTES);
			if (buffered === null) return json({ error: 'file too large', limit: MAX_UPLOAD_BYTES }, 413);
			if (buffered.byteLength === 0) return json({ error: 'no body' }, 400);
			object = await c.env.BUCKET.put(ref.key, buffered, {
				httpMetadata: { contentType },
				customMetadata: { source, job: ref.id },
			});
		}
	} catch {
		return json({ error: 'upload failed' }, 502);
	}
	if (!object) return json({ error: 'upload failed' }, 502);

	const now = nowSeconds();
	const id = await insertUpload(c.env, now, ref, {
		source,
		contentType,
		// What actually landed, not what was claimed: a lying Content-Length
		// truncates, and the potato re-verifies size and sha256 on pull.
		bytes: object.size,
		link: metadata.link ?? null,
		note: metadata.note ?? null,
		title: metadata.title ?? null,
		filename: metadata.filename ?? null,
	});
	return json({ status: 'queued', id, key: ref.key, bytes: object.size }, 200);
});

// ── the potato's side (one credential, never a door's) ──────────────────────

route('POST', '/jobs/claim', 'potato', async (c) => {
	const kinds = parseKinds(c.req.query('kinds'));
	if (kinds === null) return json({ error: 'unknown kind', allowed: KINDS }, 400);
	const jobs = await claim(c.env, nowSeconds(), { limit: parseLimit(c.req.query('limit')), kinds });
	return json({ jobs });
});

route('GET', '/jobs/pending', 'potato', async (c) => {
	return json({ jobs: await pending(c.env) });
});

route('GET', '/jobs/replay', 'potato', async (c) => {
	const raw = c.req.query('since');
	let since = nowSeconds() - 24 * 60 * 60;
	if (raw) {
		const asNumber = Number(raw);
		const asDate = Date.parse(raw);
		if (Number.isFinite(asNumber) && /^\d+$/.test(raw)) since = asNumber;
		else if (!Number.isNaN(asDate)) since = Math.floor(asDate / 1000);
		else return json({ error: 'since must be unix seconds or an ISO timestamp' }, 400);
	}
	return json({ since, jobs: await replay(c.env, since) });
});

route('POST', '/jobs/:id/done', 'potato', async (c) => {
	const result = await markDone(c.env, nowSeconds(), c.req.param('id') ?? '');
	if (result === 'missing') return json({ error: 'no such job' }, 404);
	return json({ status: 'ok' });
});

route('POST', '/jobs/:id/failed', 'potato', async (c) => {
	let error = 'failed';
	try {
		const body = (await c.req.json()) as { error?: string };
		if (typeof body?.error === 'string' && body.error) error = body.error;
	} catch {
		// A failed report without a reason is still a failed report.
	}
	const result = await markFailed(c.env, nowSeconds(), c.req.param('id') ?? '', error);
	if (result === 'missing') return json({ error: 'no such job' }, 404);
	return json({ status: 'ok' });
});

// ── backups ─────────────────────────────────────────────────────────────────
// The potato holds no R2 credential (design.md §4), so the only copy that
// survives the potato arrives here. Upload and retention only: there is no route
// that streams a backup back out. A restore pulls with the presigned GET at
// /presign, which is the same capability media reads use — the bucket stays
// private, and the Worker never becomes a read surface for the library.

route('PUT', '/backups/:name', 'potato', async (c) => {
	const name = c.req.param('name')!;
	// R2 will not take an unfixed-length stream, so the length is required rather
	// than counted as it streams: a request without one is refused here, with a
	// line that says why, instead of failing inside put() with a TypeError. The
	// Go client always sends it, because it always uploads a file.
	const declared = c.req.header('content-length') ?? null;
	if (declared === null) return json({ error: 'content-length required' }, 411);
	const bytes = Number(declared);
	if (!Number.isFinite(bytes) || bytes <= 0) return json({ error: 'no body' }, 400);
	if (bytes > BACKUP_LIMIT_BYTES) {
		return json({ error: 'backup too large', limit: BACKUP_LIMIT_BYTES, got: bytes }, 413);
	}
	const sha256 = c.req.header('x-harbor-sha256') ?? '';
	if (!/^[0-9a-f]{64}$/.test(sha256)) return json({ error: 'x-harbor-sha256 required' }, 400);
	const schema = Number(c.req.header('x-harbor-schema') ?? '0');
	if (!Number.isInteger(schema) || schema < 0) return json({ error: 'x-harbor-schema required' }, 400);
	const body = c.req.raw.body;
	if (!body) return json({ error: 'no body' }, 400);

	try {
		const summary = await putBackup(c.env, name, body, { sha256, schema });
		return json(summary, 201);
	} catch (err) {
		if (err instanceof NameError) return json({ error: 'invalid backup name', name }, 400);
		throw err;
	}
});

route('GET', '/backups', 'potato', async (c) => {
	return json({ backups: await listBackups(c.env) });
});

route('DELETE', '/backups/:name', 'potato', async (c) => {
	const name = c.req.param('name')!;
	try {
		const deleted = await deleteBackup(c.env, name);
		if (!deleted) return json({ error: 'not found', name }, 404);
		return json({ deleted: true, name });
	} catch (err) {
		if (err instanceof NameError) return json({ error: 'invalid backup name', name }, 400);
		throw err;
	}
});

route('GET', '/presign', 'potato', async (c) => {
	const key = c.req.query('key') ?? '';
	const scope = presignKeyScope(key);
	if (!scope) return json({ error: 'key not allowed' }, 400);
	// A job blob is only readable if its row still exists. A backup is proved by
	// its name: the snapshots have no D1 row, and adding one would make the
	// database a prerequisite for restoring the database (issue #60).
	if (scope === 'job' && !(await knownKey(c.env, key))) return json({ error: 'unknown key' }, 404);
	if (!presignConfigured(c.env)) {
		// No R2 credential yet: say so instead of signing something that will 403.
		return json({ error: 'presign is not configured' }, 503);
	}
	return json(await presign(c.env, key, bucketFor(c.env, scope), nowSeconds()));
});

// ── no read path, and no surprises ──────────────────────────────────────────

route('GET', '/healthz', 'none', (c) => {
	// Static, no D1 read, no blocking call: this is what the potato and a probe check.
	return json({ status: 'ok', version: c.env.VERSION ?? 'dev' });
});

app.notFound(() => json({ error: 'not found' }, 404));
app.onError((err, c) => {
	// Never a stack trace and never a 200: the door's client retries a 5xx.
	console.error(JSON.stringify({ level: 'error', message: String(err), path: c.req.path }));
	return json({ error: 'internal' }, 500);
});

// ── crons ───────────────────────────────────────────────────────────────────

async function scheduled(controller: ScheduledController, env: Env): Promise<void> {
	const now = nowSeconds();
	if (controller.cron === '*/5 * * * *') {
		const result = await reclaimLeases(env, now);
		if (result.requeued || result.failed) {
			console.log(JSON.stringify({ level: 'info', cron: 'reclaim', ...result }));
		}
		return;
	}
	if (controller.cron === '17 3 * * *') {
		// The default is seven days; the var exists so an operator can sweep sooner
		// after a known-bad insert instead of waiting a week.
		const age = Number(env.ORPHAN_AGE_SECONDS ?? Number.NaN);
		const limits = await pruneDoorLimits(env, now);
		const result = await sweepOrphans(env, now, Number.isFinite(age) && age > 0 ? age : undefined);
		console.log(JSON.stringify({ level: 'info', cron: 'sweep', limitsPruned: limits, ...result }));
		return;
	}
	// An unrecognised trigger is a wrangler.toml mistake, not a reason to guess.
	console.log(JSON.stringify({ level: 'warn', cron: controller.cron, message: 'unhandled cron' }));
}

export default {
	fetch: app.fetch,
	scheduled,
} satisfies ExportedHandler<Env>;
