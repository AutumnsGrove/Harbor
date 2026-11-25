import { beforeEach, describe, expect, it } from 'vitest';
import { PRESIGN_TTL_SECONDS } from '../src/constants';
import type { Env } from '../src/types';
import { call, env, resetAll, seedJob, seedTokens, TOKENS } from './helpers';

beforeEach(async () => {
	await resetAll();
	await seedTokens();
});

function configured(): Env {
	return {
		...(env as unknown as Env),
		R2_ACCOUNT_ID: 'abc123account',
		R2_ACCESS_KEY_ID: 'AKIDEXAMPLE',
		R2_SECRET_ACCESS_KEY: 'secret-do-not-log',
	} as Env;
}

describe('presign', () => {
	it('signs a known key for a short, bounded time', async () => {
		const id = await seedJob({ kind: 'upload', withBlob: true });
		const before = Math.floor(Date.now() / 1000);
		const res = await call('GET', `/presign?key=incoming/${id}/0`, { token: TOKENS.potato, env: configured() });
		expect(res.res.status, res.text).toBe(200);
		const body = res.json<{ url: string; expires_at: number }>();
		const url = new URL(body.url);
		expect(url.host).toBe('abc123account.r2.cloudflarestorage.com');
		expect(url.pathname).toBe(`/harbor-intake/incoming/${id}/0`);
		expect(url.searchParams.get('X-Amz-Expires')).toBe(String(PRESIGN_TTL_SECONDS));
		expect(url.searchParams.get('X-Amz-Signature')).toBeTruthy();
		expect(url.searchParams.get('X-Amz-Algorithm')).toBe('AWS4-HMAC-SHA256');
		expect(PRESIGN_TTL_SECONDS).toBeLessThanOrEqual(300);
		expect(body.expires_at).toBeGreaterThanOrEqual(before + PRESIGN_TTL_SECONDS - 2);
		expect(body.expires_at).toBeLessThanOrEqual(before + PRESIGN_TTL_SECONDS + 2);
	});

	it('refuses a key no job points at', async () => {
		const res = await call('GET', '/presign?key=incoming/not-a-job/0', { token: TOKENS.potato, env: configured() });
		expect(res.res.status).toBe(404);
	});

	it('signs a backup with no D1 row, because its name is the proof', async () => {
		const res = await call('GET', '/presign?key=backups/harbor-2026-11-24T03-10-00Z.db.zst', {
			token: TOKENS.potato,
			env: configured(),
		});
		expect(res.res.status, res.text).toBe(200);
		const body = res.json<{ url: string; expires_at: number }>();
		expect(new URL(body.url).pathname).toBe('/harbor-intake/backups/harbor-2026-11-24T03-10-00Z.db.zst');
		expect(new URL(body.url).searchParams.get('X-Amz-Signature')).toBeTruthy();
	});

	it('refuses a backup key whose name is not a snapshot', async () => {
		for (const key of ['backups/x', 'backups/harbor.db.zip', 'backups/../etc/passwd', 'backups/']) {
			const res = await call('GET', `/presign?key=${encodeURIComponent(key)}`, {
				token: TOKENS.potato,
				env: configured(),
			});
			expect(res.res.status, key).toBe(400);
		}
	});

	it('refuses traversal and unknown prefixes before touching the bucket', async () => {
		for (const key of ['../secrets', 'incoming/../../x', '/incoming/a/0', 'backups/x', '']) {
			const res = await call('GET', `/presign?key=${encodeURIComponent(key)}`, {
				token: TOKENS.potato,
				env: configured(),
			});
			expect(res.res.status, key).toBe(400);
		}
	});

	it('says so when the R2 credential is missing, instead of signing garbage', async () => {
		const id = await seedJob({ kind: 'upload', withBlob: true });
		const res = await call('GET', `/presign?key=incoming/${id}/0`, { token: TOKENS.potato });
		expect(res.res.status).toBe(503);
	});
});
