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

	it('uploads the import file as the body, not as multipart', async () => {
		const fetch = vi.fn(async () => respond({ running: true, resumable: false, run: null }, { status: 202 }));
		vi.stubGlobal('fetch', fetch);

		const file = new Blob(['id,url\n'], { type: 'text/csv' });
		await api.import.start(file);

		const [path, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
		expect(path).toBe('/api/import');
		expect(init.method).toBe('POST');
		// The file is the request: no FormData, no field name, nothing a browser
		// has to spell right.
		expect(init.body).toBe(file);
		expect((init.headers as Record<string, string>)['content-type']).toBe('application/octet-stream');
	});

	it('asks for enrichment only when it is wanted', async () => {
		const fetch = vi.fn(async () => respond({ running: true, resumable: false, run: null }, { status: 202 }));
		vi.stubGlobal('fetch', fetch);

		await api.import.start(new Blob(['x']), { enrich: true });

		expect((fetch.mock.calls[0] as unknown as [string])[0]).toBe('/api/import?enrich=1');
	});

	it('dry-runs through the same upload without starting a run', async () => {
		const fetch = vi.fn(async () =>
			respond({ dryRun: true, bytesTotal: 9, report: { total: 2, imported: 2 } })
		);
		vi.stubGlobal('fetch', fetch);

		const answer = await api.import.dryRun(new Blob(['id,url\n']));

		expect((fetch.mock.calls[0] as unknown as [string])[0]).toBe('/api/import?dryRun=1');
		expect(answer.dryRun).toBe(true);
		expect(answer.report.total).toBe(2);
	});

	it('reads the poll, including a run left behind by a restart', async () => {
		vi.stubGlobal(
			'fetch',
			vi.fn(async () =>
				respond({
					running: false,
					resumable: true,
					run: { id: 'r1', state: 'running', checkpoint: 500, startedAt: '2026-11-22T10:00:00Z' }
				})
			)
		);

		const status = await api.import.status();

		expect(status.running).toBe(false);
		expect(status.resumable).toBe(true);
		expect(status.run?.checkpoint).toBe(500);
	});

	it('cancels with a POST and nothing else', async () => {
		const fetch = vi.fn(async () => respond({ status: 'cancelling' }, { status: 202 }));
		vi.stubGlobal('fetch', fetch);

		await expect(api.import.cancel()).resolves.toEqual({ status: 'cancelling' });

		const [path, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
		expect(path).toBe('/api/import/cancel');
		expect(init.method).toBe('POST');
	});
});
