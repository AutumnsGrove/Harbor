import { describe, expect, it } from 'vitest';
import { call, env, seedTokens, TOKENS } from './helpers';
import type { Env } from '../src/types';

// plans/12a: the potato holds no R2 credential, so this route is the only way a
// snapshot leaves it. What matters: door tokens cannot reach it, no route reads a
// backup back out, and a name is validated as an R2 key before it becomes one.

const SHA = 'a'.repeat(64);

const BODY = 'zstdbytes';

function backup(name: string, headers: Record<string, string> = {}): Promise<{ res: Response }> {
	return call('PUT', `/backups/${name}`, {
		token: TOKENS.potato,
		body: BODY,
		headers: {
			// R2 needs a known length for a streamed body, so the upload requires
			// it — and so does this helper.
			'content-length': String(BODY.length),
			'x-harbor-sha256': SHA,
			'x-harbor-schema': '1',
			...headers,
		},
	});
}

describe('backup upload', () => {
	it('stores the body in the backups bucket with its checksum', async () => {
		await seedTokens();
		const { res } = await backup('harbor-20261122T180000Z.db.zst');
		expect(res.status).toBe(201);

		const object = await (env as unknown as Env).BACKUPS.get('backups/harbor-20261122T180000Z.db.zst');
		expect(object).not.toBeNull();
		expect(await object!.text()).toBe('zstdbytes');
		expect(object!.customMetadata?.sha256).toBe(SHA);
		expect(object!.customMetadata?.schema).toBe('1');
	});

	it('refuses a door token', async () => {
		await seedTokens();
		// Its own name: the bucket is shared by the tests in this file.
		const { res } = await call('PUT', '/backups/harbor-20261120T180000Z.db.zst', {
			token: TOKENS.shortcut,
			body: BODY,
			headers: { 'content-length': String(BODY.length), 'x-harbor-sha256': SHA, 'x-harbor-schema': '1' },
		});
		expect(res.status).toBe(403);
		expect(await (env as unknown as Env).BACKUPS.head('backups/harbor-20261120T180000Z.db.zst')).toBeNull();
	});

	it('refuses no token at all', async () => {
		await seedTokens();
		const { res } = await call('PUT', '/backups/harbor-20261122T180000Z.db.zst', {
			body: BODY,
			headers: { 'content-length': String(BODY.length), 'x-harbor-sha256': SHA, 'x-harbor-schema': '1' },
		});
		expect(res.status).toBe(401);
	});

	it('refuses a name that is not a backup', async () => {
		await seedTokens();
		for (const name of ['notes.txt', 'harbor.db']) {
			const { res } = await backup(name);
			expect(res.status, name).toBe(400);
		}
		// A traversal is not even a route match: the name never becomes a key.
		const escape = await backup('..%2Fescape.db.zst');
		expect([400, 404]).toContain(escape.res.status);
		expect(await (env as unknown as Env).BACKUPS.list({ prefix: 'backups/../' })).toMatchObject({ objects: [] });
	});

	it('requires the checksum and the schema version', async () => {
		await seedTokens();
		const noSha = await call('PUT', '/backups/harbor-20261122T180000Z.db.zst', {
			token: TOKENS.potato,
			body: BODY,
			headers: { 'content-length': String(BODY.length), 'x-harbor-schema': '1' },
		});
		expect(noSha.res.status).toBe(400);
	});

	it('refuses a body over the cap', async () => {
		await seedTokens();
		const { res } = await call('PUT', '/backups/harbor-20261122T180000Z.db.zst', {
			token: TOKENS.potato,
			body: 'zstdbytes',
			headers: { 'x-harbor-sha256': SHA, 'x-harbor-schema': '1', 'content-length': String(200 * 1024 * 1024) },
		});
		expect(res.status).toBe(413);
	});
});

describe('backup listing and deletion', () => {
	it('lists what is there, newest name first', async () => {
		await seedTokens();
		await backup('harbor-20261121T180000Z.db.zst');
		await backup('harbor-20261122T180000Z.db.zst');

		const { res, json } = await call('GET', '/backups', { token: TOKENS.potato });
		expect(res.status).toBe(200);
		const body = json<{ backups: { name: string; bytes: number }[] }>();
		expect(body.backups.map((b) => b.name)).toEqual([
			'harbor-20261122T180000Z.db.zst',
			'harbor-20261121T180000Z.db.zst',
		]);
		expect(body.backups[0]!.bytes).toBe('zstdbytes'.length);
	});

	it('deletes one and says so, twice', async () => {
		await seedTokens();
		await backup('harbor-20261122T180000Z.db.zst');

		const first = await call('DELETE', '/backups/harbor-20261122T180000Z.db.zst', { token: TOKENS.potato });
		expect(first.res.status).toBe(200);
		expect(await (env as unknown as Env).BACKUPS.head('backups/harbor-20261122T180000Z.db.zst')).toBeNull();

		const second = await call('DELETE', '/backups/harbor-20261122T180000Z.db.zst', { token: TOKENS.potato });
		expect(second.res.status).toBe(404);
	});

	it('refuses a door token on list and delete', async () => {
		await seedTokens();
		await backup('harbor-20261122T180000Z.db.zst');
		expect((await call('GET', '/backups', { token: TOKENS.shortcut })).res.status).toBe(403);
		expect((await call('DELETE', '/backups/harbor-20261122T180000Z.db.zst', { token: TOKENS.shortcut })).res.status).toBe(403);
	});
});
