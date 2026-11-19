import { beforeEach, describe, expect, it } from 'vitest';
import { call, env, resetAll, incomingObjects, jobRow, seedTokens, TOKENS } from './helpers';

beforeEach(async () => {
	await resetAll();
	await seedTokens();
});

async function save(body: string, token = TOKENS.shortcut) {
	return call('POST', '/save', { token, body });
}

describe('POST /save', () => {
	it('answers 200 and writes only D1', async () => {
		const before = await incomingObjects();
		const res = await save('{"link":"https://example.com/x"}');
		expect(res.res.status).toBe(200);
		expect(res.json<{ status: string }>().status).toBe('queued');
		expect(await incomingObjects()).toEqual(before);
	});

	it('rejects an empty body', async () => {
		expect((await save('')).res.status).toBe(400);
		expect((await save('   ')).res.status).toBe(400);
	});

	it('rejects JSON that is not an object with a link or a note', async () => {
		expect((await save('{}')).res.status).toBe(400);
		expect((await save('[]')).res.status).toBe(400);
		expect((await save('"a string"')).res.status).toBe(400);
	});

	it('rejects a body over the 96 KB cap', async () => {
		const big = JSON.stringify({ note: 'a'.repeat(100 * 1024) });
		const res = await save(big);
		expect(res.res.status).toBe(413);
	});

	it('rejects a note over the cap even with no declared length', async () => {
		// Whatever the transport says, the parsed body is checked too.
		const note = 'a'.repeat(65537);
		const res = await save(JSON.stringify({ note }));
		expect(res.res.status).toBe(400);
	});

	it('rejects a non-http link', async () => {
		for (const link of ['javascript:alert(1)', 'ftp://example.com/x', 'example.com', '//example.com']) {
			const res = await save(JSON.stringify({ link }));
			expect(res.res.status, link).toBe(400);
		}
	});

	it('ignores unknown fields instead of refusing them', async () => {
		const res = await save('{"link":"https://example.com/y","future":"whatever"}');
		expect(res.res.status).toBe(200);
	});

	it('records a note-only save as kind note and a link save as kind link', async () => {
		const note = await save('{"note":"a thought"}');
		expect((await jobRow(note.json<{ id: string }>().id))?.kind).toBe('note');
		const link = await save('{"link":"https://example.com/z","note":"a thought"}');
		expect((await jobRow(link.json<{ id: string }>().id))?.kind).toBe('link');
	});

	it('keeps the raw link: canonicalisation is the potato job', async () => {
		const raw = 'https://EXAMPLE.com/Path?utm_source=x#frag';
		const res = await save(JSON.stringify({ link: raw }));
		expect((await jobRow(res.json<{ id: string }>().id))?.link).toBe(raw);
	});

	it('sets touched_at and created_at to now, and leaves the lease null', async () => {
		const before = Math.floor(Date.now() / 1000) - 2;
		const res = await save('{"link":"https://example.com/t"}');
		const row = await jobRow(res.json<{ id: string }>().id);
		expect(Number(row?.created_at)).toBeGreaterThanOrEqual(before);
		expect(Number(row?.touched_at)).toBe(Number(row?.created_at));
		expect(row?.lease_until).toBeNull();
		expect(row?.attempts).toBe(0);
	});
});
