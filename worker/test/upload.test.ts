import { beforeEach, describe, expect, it } from 'vitest';
import { call, env, resetAll, incomingObjects, jobRow, recordEnv, seedTokens, TOKENS } from './helpers';

beforeEach(async () => {
	await resetAll();
	await seedTokens();
});

const JPEG = new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4]);

function upload(body: BodyInit, headers: Record<string, string>, token = TOKENS.shortcut) {
	return call('POST', '/upload', { token, body, headers });
}

describe('POST /upload', () => {
	it('writes the R2 object before the D1 row', async () => {
		const order: string[] = [];
		const res = await upload(JPEG, { 'content-type': 'image/jpeg' }, TOKENS.shortcut);
		expect(res.res.status, res.text).toBe(200);

		const recorded: string[] = [];
		const res2 = await call('POST', '/upload', {
			token: TOKENS.shortcut,
			body: JPEG,
			headers: { 'content-type': 'image/jpeg' },
			env: recordEnv(recorded),
		});
		expect(res2.res.status).toBe(200);
		const r2At = recorded.indexOf('r2.put');
		const d1At = recorded.indexOf('d1.batch');
		expect(r2At).toBeGreaterThanOrEqual(0);
		expect(d1At).toBeGreaterThan(r2At);
		void order;
	});

	it('records the blob, its type and its real size', async () => {
		const res = await upload(JPEG, { 'content-type': 'image/jpeg' });
		const body = res.json<{ id: string; key: string; bytes: number }>();
		expect(body.bytes).toBe(JPEG.byteLength);
		const row = await jobRow(body.id);
		expect(row?.kind).toBe('upload');
		const blob = await env.DB.prepare('SELECT * FROM blobs WHERE job_id = ?')
			.bind(body.id)
			.first<{ r2_key: string; content_type: string; bytes: number; n: number }>();
		expect(blob?.r2_key).toBe(`incoming/${body.id}/0`);
		expect(blob?.content_type).toBe('image/jpeg');
		expect(blob?.bytes).toBe(JPEG.byteLength);
		expect(blob?.n).toBe(0);
		const object = await env.BUCKET.head(`incoming/${body.id}/0`);
		expect(object?.size).toBe(JPEG.byteLength);
	});

	it('rejects a content type outside the allowlist with 415 and writes nothing', async () => {
		const before = await incomingObjects();
		const res = await upload(new Uint8Array([1, 2, 3]), { 'content-type': 'application/zip' });
		expect(res.res.status).toBe(415);
		expect(await incomingObjects()).toEqual(before);
	});

	it('accepts the allowlist: pdf, text, markdown and every image/*', async () => {
		for (const type of ['application/pdf', 'text/plain', 'text/markdown', 'image/png', 'image/svg+xml', 'image/heic']) {
			const res = await upload(JPEG, { 'content-type': type });
			expect(res.res.status, type).toBe(200);
		}
	});

	it('rejects an empty body', async () => {
		const res = await upload(new Uint8Array([]), { 'content-type': 'image/jpeg' });
		expect(res.res.status).toBe(400);
	});

	it('rejects a file over 25 MB before writing it', async () => {
		const before = await incomingObjects();
		const big = new Uint8Array(25 * 1024 * 1024 + 1);
		const res = await upload(big, { 'content-type': 'image/jpeg' });
		expect(res.res.status).toBe(413);
		expect(await incomingObjects()).toEqual(before);
	});

	it('keeps the metadata the door sent', async () => {
		const res = await call('POST', '/upload?link=https%3A%2F%2Fexample.com%2Fpost&filename=cat.jpg&note=look', {
			token: TOKENS.shortcut,
			body: JPEG,
			headers: { 'content-type': 'image/jpeg' },
		});
		expect(res.res.status).toBe(200);
		const row = await jobRow(res.json<{ id: string }>().id);
		expect(row?.link).toBe('https://example.com/post');
		expect(row?.note).toBe('look');
		expect(row?.payload_json).toBe('{"filename":"cat.jpg"}');
	});

	it('rejects a bad link in the query', async () => {
		const res = await call('POST', '/upload?link=file%3A%2F%2F%2Fetc%2Fpasswd', {
			token: TOKENS.shortcut,
			body: JPEG,
			headers: { 'content-type': 'image/jpeg' },
		});
		expect(res.res.status).toBe(400);
	});
});
