import { beforeEach, describe, expect, it } from 'vitest';
import { ROUTE_SCOPES } from '../src/index';
import { sha256Hex } from '../src/auth';
import { call, env, jobRow, seedJob, seedTokens, TOKENS } from './helpers';

beforeEach(async () => {
	await seedTokens();
});

describe('identity', () => {
	it('refuses a missing token on both scopes', async () => {
		expect((await call('POST', '/save', { body: '{"link":"https://example.com"}' })).res.status).toBe(401);
		expect((await call('POST', '/jobs/claim')).res.status).toBe(401);
	});

	it('refuses a wrong token the same way as a missing one', async () => {
		const missing = await call('POST', '/save', { body: '{}' });
		const wrong = await call('POST', '/save', { token: 'not-a-token', body: '{}' });
		expect(wrong.res.status).toBe(401);
		expect(wrong.text).toBe(missing.text);
	});

	it('writes a job with the source taken from the token', async () => {
		const res = await call('POST', '/save', { token: TOKENS.shortcut, body: '{"link":"https://example.com/a"}' });
		expect(res.res.status).toBe(200);
		const body = res.json<{ id: string }>();
		const row = await jobRow(body.id);
		expect(row?.source).toBe('shortcut');
		expect(row?.status).toBe('queued');
	});

	it('does not trust a source in the body', async () => {
		const res = await call('POST', '/save', {
			token: TOKENS.shortcut,
			body: '{"link":"https://example.com/b","source":"telegram"}',
		});
		const row = await jobRow(res.json<{ id: string }>().id);
		expect(row?.source).toBe('shortcut');
	});

	it('refuses a revoked token and writes nothing', async () => {
		await env.DB.prepare('UPDATE tokens SET revoked_at = ? WHERE id = ?')
			.bind(Math.floor(Date.now() / 1000), 'shortcut')
			.run();
		const before = await env.DB.prepare('SELECT COUNT(*) AS n FROM jobs').first<{ n: number }>();
		const res = await call('POST', '/save', { token: TOKENS.shortcut, body: '{"link":"https://example.com/c"}' });
		expect(res.res.status).toBe(401);
		const after = await env.DB.prepare('SELECT COUNT(*) AS n FROM jobs').first<{ n: number }>();
		expect(after?.n).toBe(before?.n);
	});

	it('revoking one door leaves the other working', async () => {
		await env.DB.prepare('UPDATE tokens SET revoked_at = ? WHERE id = ?')
			.bind(Math.floor(Date.now() / 1000), 'shortcut')
			.run();
		expect((await call('POST', '/save', { token: TOKENS.shortcut, body: '{"link":"https://example.com/d"}' })).res.status).toBe(401);
		expect((await call('POST', '/save', { token: TOKENS.telegram, body: '{"link":"https://example.com/e"}' })).res.status).toBe(200);
	});

	it('records last_seen_at for the door that was used', async () => {
		await call('POST', '/save', { token: TOKENS.telegram, body: '{"link":"https://example.com/f"}' });
		const row = await env.DB.prepare('SELECT last_seen_at FROM tokens WHERE id = ?').bind('telegram').first<{ last_seen_at: number }>();
		expect(row?.last_seen_at).toBeTypeOf('number');
	});

	it('refuses a door whose token id is not a known source', async () => {
		await env.DB.prepare(
			'INSERT INTO tokens (id, scope, sha256, label, created_at) VALUES (?, ?, ?, ?, ?)',
		)
			.bind('ios', 'door', await sha256Hex('ios-token'), 'ios', Math.floor(Date.now() / 1000))
			.run();
		const res = await call('POST', '/save', { token: 'ios-token', body: '{"link":"https://example.com/ios"}' });
		expect(res.res.status).toBe(403);
		const rows = await env.DB.prepare("SELECT COUNT(*) AS n FROM jobs WHERE source = 'ios'").first<{ n: number }>();
		expect(rows?.n).toBe(0);
	});

	it('stores only the hash of a token', async () => {
		const stored = await env.DB.prepare('SELECT sha256 FROM tokens').all<{ sha256: string }>();
		const hashes = (stored.results ?? []).map((r) => r.sha256);
		for (const token of Object.values(TOKENS)) {
			expect(hashes).not.toContain(token);
			expect(hashes).toContain(await sha256Hex(token));
		}
	});
});

describe('scope matrix', () => {
	// Every route × every token, generated from the table the router uses. A door
	// token on a potato route is a 403 with zero side effects.
	const routes = [...ROUTE_SCOPES.entries()];

	it('declares a scope for every registered route', () => {
		expect(routes.length).toBeGreaterThanOrEqual(9);
		for (const [key, scope] of routes) {
			expect(['door', 'potato', 'none']).toContain(scope);
			expect(key).toMatch(/^(GET|POST|PUT|DELETE) \//);
		}
	});

	const cases = routes.filter(([, scope]) => scope !== 'none');

	for (const [key, scope] of cases) {
		const [method, path] = key.split(' ') as [string, string];
		const concrete = path.replace(':id', 'job-that-does-not-exist');

		it(`${key}: expects ${scope}, refuses the other token`, async () => {
			const doorToken = TOKENS.shortcut;
			const potatoToken = TOKENS.potato;
			const expectedToken = scope === 'door' ? doorToken : potatoToken;
			const wrongToken = scope === 'door' ? potatoToken : doorToken;

			const wrong = await call(method, concrete, { token: wrongToken, body: method === 'POST' ? '{}' : undefined });
			expect(wrong.res.status).toBe(403);

			const missing = await call(method, concrete, { body: method === 'POST' ? '{}' : undefined });
			expect(missing.res.status).toBe(401);

			// The expected token gets past auth: any status except 401/403 is fine,
			// because the route itself may reject the empty body.
			const right = await call(method, concrete, { token: expectedToken, body: method === 'POST' ? '{}' : undefined });
			expect([401, 403]).not.toContain(right.res.status);
		});
	}

	it('refuses a door token on claim and presign, with no side effects', async () => {
		const id = await seedJob({});
		const claimedBefore = await env.DB.prepare("SELECT COUNT(*) AS n FROM jobs WHERE status = 'claimed'").first<{ n: number }>();
		const attempts: [string, string][] = [
			['POST', '/jobs/claim'],
			['GET', '/presign?key=incoming/x/0'],
			['GET', '/jobs/pending'],
		];
		for (const [method, path] of attempts) {
			const res = await call(method, path, { token: TOKENS.shortcut, body: method === 'POST' ? '{}' : undefined });
			expect(res.res.status, `${method} ${path}`).toBe(403);
		}
		const claimedAfter = await env.DB.prepare("SELECT COUNT(*) AS n FROM jobs WHERE status = 'claimed'").first<{ n: number }>();
		expect(claimedAfter?.n).toBe(claimedBefore?.n);
		expect((await jobRow(id))?.status).toBe('queued');
	});
});
