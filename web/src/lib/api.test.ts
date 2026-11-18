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
