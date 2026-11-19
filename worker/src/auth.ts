import type { Env, Principal, Scope } from './types';

// Tokens are stored as sha256 hex. The bearer arrives in a header, is hashed, and
// looked up: no plaintext secret is ever in D1, so a dump of the database is a
// list of door names, not a list of keys.
//
// `scope` is the whole security model (design.md §4): a `door` token may append
// and nothing else. That is enforced per route in index.ts and tested by walking
// the router, so a route that forgets to declare a scope fails the suite.

export async function sha256Hex(input: string): Promise<string> {
	const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(input));
	return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, '0')).join('');
}

/** Length-safe, char-by-char compare. The values compared are hex digests. */
export function constantTimeEqual(a: string, b: string): boolean {
	if (a.length !== b.length) return false;
	let diff = 0;
	for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
	return diff === 0;
}

export function bearer(req: Request): string | null {
	const header = req.headers.get('authorization');
	if (!header) return null;
	const match = /^Bearer\s+(\S+)$/i.exec(header.trim());
	return match?.[1] ?? null;
}

interface TokenRow {
	id: string;
	scope: Scope;
	sha256: string;
	last_seen_at: number | null;
}

/**
 * Resolve the bearer to a principal, or null. A revoked token is null, not an
 * error with details: the caller gets the same 401 as a token that never existed.
 */
export async function authenticate(
	req: Request,
	env: Env,
	now: number,
	waitUntil?: (p: Promise<unknown>) => void,
): Promise<Principal | null> {
	const token = bearer(req);
	if (!token) return null;
	const hash = await sha256Hex(token);

	const row = await env.DB.prepare(
		'SELECT id, scope, sha256, last_seen_at FROM tokens WHERE sha256 = ? AND revoked_at IS NULL',
	)
		.bind(hash)
		.first<TokenRow>();

	// The lookup already proved equality; this keeps the comparison itself
	// constant-time for anyone reading the code, and refuses a row that somehow
	// hash-matches without matching.
	if (!row || !constantTimeEqual(hash, row.sha256)) return null;

	// Last seen is how Settings notices a door that silently broke. One write per
	// five minutes per token, in the background, never in the request path.
	const staleBefore = now - 300;
	if (row.last_seen_at === null || row.last_seen_at < staleBefore) {
		const touch = env.DB.prepare(
			'UPDATE tokens SET last_seen_at = ? WHERE id = ? AND (last_seen_at IS NULL OR last_seen_at < ?)',
		)
			.bind(now, row.id, staleBefore)
			.run();
		if (waitUntil) waitUntil(touch.catch(() => undefined));
		else await touch.catch(() => undefined);
	}

	return { id: row.id, scope: row.scope };
}
