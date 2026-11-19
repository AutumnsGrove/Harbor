import { createExecutionContext, env, waitOnExecutionContext } from 'cloudflare:test';
import worker from '../src/index';
import { sha256Hex } from '../src/auth';
import type { Env, Kind, Scope } from '../src/types';

export { env };

export const TOKENS = {
	shortcut: 'shortcut-token-for-tests',
	telegram: 'telegram-token-for-tests',
	potato: 'potato-token-for-tests',
};

/** Seed the three tokens the way the bootstrap script would. */
export async function seedTokens(e: Env = env as unknown as Env): Promise<void> {
	const now = Math.floor(Date.now() / 1000);
	const rows: [string, Scope, string][] = [
		['shortcut', 'door', TOKENS.shortcut],
		['telegram', 'door', TOKENS.telegram],
		['potato', 'potato', TOKENS.potato],
	];
	for (const [id, scope, token] of rows) {
		await e.DB.prepare(
			'INSERT OR REPLACE INTO tokens (id, scope, sha256, label, created_at) VALUES (?, ?, ?, ?, ?)',
		)
			.bind(id, scope, await sha256Hex(token), id, now)
			.run();
	}
}

export interface CallOptions {
	token?: string | null;
	body?: string | BodyInit | null;
	headers?: Record<string, string>;
	env?: Env;
}

export interface CallResult {
	res: Response;
	text: string;
	json: <T = Record<string, unknown>>() => T;
}

export async function call(method: string, path: string, opts: CallOptions = {}): Promise<CallResult> {
	const headers: Record<string, string> = { ...(opts.headers ?? {}) };
	if (opts.token) headers['authorization'] = `Bearer ${opts.token}`;
	if (typeof opts.body === 'string' && !headers['content-type'] && method !== 'GET') {
		headers['content-type'] = 'application/json';
	}
	const ctx = createExecutionContext();
	const req = new Request(`https://harbor.test${path}`, {
		method,
		headers,
		body: opts.body ?? undefined,
	});
	const res = await worker.fetch(req, (opts.env ?? env) as unknown as Env, ctx);
	await waitOnExecutionContext(ctx);
	const text = await res.text();
	return {
		res,
		text,
		json: <T,>() => JSON.parse(text) as T,
	};
}

export function now(): number {
	return Math.floor(Date.now() / 1000);
}

/** Insert a job by hand, for the states the API cannot produce directly. */
export async function seedJob(
	overrides: Partial<{
		id: string;
		source: string;
		kind: Kind;
		link: string | null;
		note: string | null;
		group_key: string | null;
		touched_at: number;
		status: string;
		attempts: number;
		claimed_at: number | null;
		lease_until: number | null;
		created_at: number;
		done_at: number | null;
		withBlob: boolean;
	}> = {},
	e: Env = env as unknown as Env,
): Promise<string> {
	const t = now();
	const row = {
		id: overrides.id ?? crypto.randomUUID(),
		source: overrides.source ?? 'shortcut',
		kind: overrides.kind ?? 'link',
		link: overrides.link ?? 'https://example.com/x',
		note: overrides.note ?? null,
		group_key: overrides.group_key ?? null,
		touched_at: overrides.touched_at ?? t,
		status: overrides.status ?? 'queued',
		attempts: overrides.attempts ?? 0,
		claimed_at: overrides.claimed_at ?? null,
		lease_until: overrides.lease_until ?? null,
		created_at: overrides.created_at ?? t,
		done_at: overrides.done_at ?? null,
	};
	await e.DB.prepare(
		`INSERT INTO jobs (id, source, kind, link, note, group_key, touched_at, status, attempts,
		 claimed_at, lease_until, created_at, updated_at, done_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
		.bind(
			row.id,
			row.source,
			row.kind,
			row.link,
			row.note,
			row.group_key,
			row.touched_at,
			row.status,
			row.attempts,
			row.claimed_at,
			row.lease_until,
			row.created_at,
			row.created_at,
			row.done_at,
		)
		.run();
	if (overrides.withBlob) {
		await e.DB.prepare(
			'INSERT INTO blobs (job_id, n, r2_key, content_type, bytes, created_at) VALUES (?, 0, ?, ?, ?, ?)',
		)
			.bind(row.id, `incoming/${row.id}/0`, 'image/jpeg', 1024, row.created_at)
			.run();
	}
	return row.id;
}

export async function jobRow(id: string, e: Env = env as unknown as Env) {
	return e.DB.prepare('SELECT * FROM jobs WHERE id = ?').bind(id).first<Record<string, unknown>>();
}

/**
 * Wrap the real bindings so a test can assert the *order* of writes. D1 cannot
 * promise "the object exists before the row"; the call order is the only way to
 * see it.
 */
export function recordEnv(order: string[], e: Env = env as unknown as Env): Env {
	const wrap = <T extends object>(target: T, label: string, methods: string[]): T =>
		new Proxy(target, {
			get(obj, prop, receiver) {
				const value = Reflect.get(obj, prop, obj);
				if (typeof prop === 'string' && methods.includes(prop)) {
					return (...args: unknown[]) => {
						order.push(`${label}.${prop}`);
						return (value as (...a: unknown[]) => unknown).apply(obj, args);
					};
				}
				return typeof value === 'function' ? (value as (...a: unknown[]) => unknown).bind(obj) : value;
			},
		});
	return {
		...e,
		BUCKET: wrap(e.BUCKET, 'r2', ['put', 'delete', 'list', 'head', 'get']),
		DB: wrap(e.DB, 'd1', ['batch', 'prepare', 'exec']),
	} as Env;
}

/** Every object currently under incoming/, for "did anything land in R2" checks. */
export async function incomingObjects(e: Env = env as unknown as Env): Promise<string[]> {
	const page = await e.BUCKET.list({ prefix: 'incoming/' });
	return page.objects.map((o) => o.key);
}

/** Empty D1 and R2 between tests in a file: storage is per file, not per test. */
export async function resetAll(e: Env = env as unknown as Env): Promise<void> {
	await e.DB.batch([
		e.DB.prepare('DELETE FROM blobs'),
		e.DB.prepare('DELETE FROM jobs'),
		e.DB.prepare('DELETE FROM tokens'),
	]);
	let cursor: string | undefined;
	do {
		const page = await e.BUCKET.list({ cursor, limit: 1000 });
		for (const object of page.objects) await e.BUCKET.delete(object.key);
		cursor = page.truncated ? (page.cursor as string) : undefined;
	} while (cursor);
}
