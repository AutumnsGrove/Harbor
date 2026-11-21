import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError, api, isApiError } from './api';

function respond(body: unknown, init: ResponseInit = { status: 200 }) {
	return new Response(typeof body === 'string' ? body : JSON.stringify(body), {
		headers: { 'content-type': 'application/json' },
		...init
	});
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe('the API client', () => {
	it('maps a 403 to a typed error, not a generic failure', async () => {
		vi.stubGlobal('fetch', vi.fn(async () => respond({ error: 'identity not recognised' }, { status: 403 })));

		const failure = await api.version().catch((error: unknown) => error);

		expect(isApiError(failure)).toBe(true);
		expect(failure).toBeInstanceOf(ApiError);
		expect((failure as ApiError).status).toBe(403);
		// The server's own sentence is what the shell shows; it is more useful
		// than "Forbidden".
		expect((failure as ApiError).message).toBe('identity not recognised');
	});

	it('keeps the status when the error body is not JSON', async () => {
		vi.stubGlobal('fetch', vi.fn(async () => respond('<html>bad gateway</html>', { status: 502, statusText: 'Bad Gateway' })));

		const failure = await api.version().catch((error: unknown) => error);

		expect(isApiError(failure)).toBe(true);
		expect((failure as ApiError).status).toBe(502);
		expect((failure as ApiError).message).toContain('502');
	});

	it('parses a successful response', async () => {
		vi.stubGlobal('fetch', vi.fn(async () => respond({ version: 'r412.1a2b3c4', deployment: 'docker' })));

		await expect(api.version()).resolves.toEqual({ version: 'r412.1a2b3c4', deployment: 'docker' });
	});

	it('sends the composer\'s save as JSON and returns the merged flag', async () => {
		const fetch = vi.fn(async () =>
			respond({ item: { id: 'i1', title: 'Example' }, merged: true }, { status: 201 })
		);
		vi.stubGlobal('fetch', fetch);

		const saved = await api.items.create({ url: 'https://example.com', note: 'later' });

		expect(saved.merged).toBe(true);
		const [path, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
		expect(path).toBe('/api/items');
		expect(init.method).toBe('POST');
		expect(JSON.parse(String(init.body))).toEqual({ url: 'https://example.com', note: 'later' });
		expect((init.headers as Record<string, string>)['content-type']).toBe('application/json');
	});

	it('treats 204 as a successful delete', async () => {
		vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 204 })));

		await expect(api.items.remove('i1')).resolves.toBeUndefined();
	});

	it('pages the library with an encoded cursor', async () => {
		const fetch = vi.fn(async () => respond({ items: [], next: null }));
		vi.stubGlobal('fetch', fetch);

		await api.items.list({ limit: 20, cursor: 'abc+/=', deleted: true });

		const [path] = fetch.mock.calls[0] as unknown as [string, RequestInit];
		expect(path).toBe('/api/items?limit=20&cursor=abc%2B%2F%3D&deleted=true');
	});

	it('reads the pending window through the potato', async () => {
		const fetch = vi.fn(async () => respond({ jobs: [{ id: 'job-1', title: 'x' }] }));
		vi.stubGlobal('fetch', fetch);

		await expect(api.pending.list()).resolves.toEqual({ jobs: [{ id: 'job-1', title: 'x' }] });
		expect((fetch.mock.calls[0] as unknown as [string])[0]).toBe('/api/pending');
	});

	it('posts to the update endpoints without a body', async () => {
		const fetch = vi.fn(async () => respond({ status: 'checking' }, { status: 202 }));
		vi.stubGlobal('fetch', fetch);

		await expect(api.update.check()).resolves.toEqual({ status: 'checking' });

		const [path, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
		expect(path).toBe('/api/update/check');
		expect(init.method).toBe('POST');
		expect(init.body).toBeUndefined();
	});
});
