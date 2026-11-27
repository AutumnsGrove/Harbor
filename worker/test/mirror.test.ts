import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { call, env, seedTokens, TOKENS } from './helpers';
import { ROUTE_SCOPES } from '../src/index';
import { isChecksumMismatch, mirrorKeyFor, validMirrorKey } from '../src/mirror';
import type { Env } from '../src/types';

// plans/06-media test 10 and this phase's mirror task: the potato holds the only
// original of an in-app save, and this route is how a second copy reaches R2.
// What matters: the key is the content address (so a retry is one more PUT of
// the same bytes), a door token cannot reach it, a bad address is refused before
// anything is written, and no route reads a blob back out.

// The address has to be the real SHA-256 of the body: R2 checks it, so a
// made-up one would only prove the check is absent. That is exactly how the
// first version of these tests missed the defect review found.
const BODY = 'jpegbytes';
const sha256 = (text: string) => createHash('sha256').update(text).digest('hex');
const HASH = sha256(BODY);
const PREFIX = HASH.slice(0, 2);
const OTHER_BODY = 'different bytes entirely';
/** The R2 key keeps the fan-out: media/<2 hex>/<64 hex> (plans/06-media). */
const keyOf = (hash: string) => `media/${hash.slice(0, 2)}/${hash}`;

function mirror(
	prefix = PREFIX,
	hash = HASH,
	headers: Record<string, string> = {},
	token = TOKENS.potato,
	body = BODY,
) {
	return call('PUT', `/mirror/${prefix}/${hash}`, {
		token,
		body,
		headers: {
			// R2 needs a known length for a streamed body.
			'content-length': String(body.length),
			'content-type': 'image/jpeg',
			...headers,
		},
	});
}

describe('mirror-up', () => {
	it('stores the blob at its content address', async () => {
		await seedTokens();
		const { res, json } = await mirror();
		expect(res.status).toBe(201);
		const summary = json<{ key: string; bytes: number }>();
		expect(summary.key).toBe(keyOf(HASH));
		expect(summary.bytes).toBe(BODY.length);

		const object = await (env as unknown as Env).BUCKET.get(keyOf(HASH));
		expect(object).not.toBeNull();
		expect(await object!.text()).toBe('jpegbytes');
		expect(object!.customMetadata?.sha256).toBe(HASH);
		expect(object!.httpMetadata?.contentType).toBe('image/jpeg');
	});

	// Idempotence is the whole reason the key is a content address: a retry after
	// a dropped connection must cost one PUT and leave one object.
	it('is idempotent: the same bytes twice are one object', async () => {
		await seedTokens();
		const hash = sha256(OTHER_BODY);
		const first = await mirror(hash.slice(0, 2), hash, {}, TOKENS.potato, OTHER_BODY);
		expect(first.res.status).toBe(201);
		const second = await mirror(hash.slice(0, 2), hash, {}, TOKENS.potato, OTHER_BODY);
		expect(second.res.status).toBe(201);
		expect(await (await (env as unknown as Env).BUCKET.get(keyOf(hash)))!.text()).toBe(OTHER_BODY);
	});

	it('refuses a door token and writes nothing', async () => {
		await seedTokens();
		const hash = '11' + 'f'.repeat(62);
		const { res } = await mirror(hash.slice(0, 2), hash, {}, TOKENS.shortcut);
		expect(res.status).toBe(403);
		expect(await (env as unknown as Env).BUCKET.head(keyOf(hash))).toBeNull();
	});

	it('refuses a missing token', async () => {
		await seedTokens();
		const hash = '22' + 'f'.repeat(62);
		const { res } = await call('PUT', `/mirror/${hash.slice(0, 2)}/${hash}`, {
			body: BODY,
			headers: { 'content-length': String(BODY.length) },
		});
		expect(res.status).toBe(401);
	});

	// The prefix is part of the key, so a disagreement between it and the hash
	// would file an object under a name its own contents contradict — and no
	// later sweep could repair it.
	it('refuses a prefix that disagrees with the hash', async () => {
		await seedTokens();
		const hash = '33' + 'f'.repeat(62);
		const { res } = await mirror('44', hash);
		expect(res.status).toBe(400);
		expect(await (env as unknown as Env).BUCKET.head(keyOf(hash))).toBeNull();
	});

	it('refuses addresses that are not content addresses', async () => {
		await seedTokens();
		// Traversal is not in this list on purpose: a URL with ".." in it never
		// reaches this route (the request path is normalised first), so the guard
		// that matters for it is validMirrorKey, asserted below.
		const cases: [string, string][] = [
			['33', 'nothashenough'], // short hash
			['33', 'G'.repeat(64)], // not hex
			['33', 'F'.repeat(64)], // uppercase: the potato hashes lowercase
			['33', 'f'.repeat(63)], // one character short
		];
		for (const [prefix, hash] of cases) {
			const { res } = await mirror(prefix, hash);
			expect(res.status, `${prefix}/${hash.slice(0, 8)}`).toBe(400);
		}
	});

	it('requires a content length, because R2 will not take an unbounded stream', async () => {
		await seedTokens();
		const hash = '55' + 'f'.repeat(62);
		const { res } = await call('PUT', `/mirror/${hash.slice(0, 2)}/${hash}`, {
			token: TOKENS.potato,
			body: BODY,
		});
		expect(res.status).toBe(411);
	});

	it('refuses a blob over the upload cap with the number it saw', async () => {
		await seedTokens();
		const hash = '66' + 'f'.repeat(62);
		const { res, json } = await mirror(hash.slice(0, 2), hash, {
			'content-length': String(26 * 1024 * 1024),
		});
		expect(res.status).toBe(413);
		const body = json<{ limit: number; got: number }>();
		expect(body.limit).toBe(25 * 1024 * 1024);
		expect(body.got).toBe(26 * 1024 * 1024);
	});

	// The route is in the map that the scope audit reads, at potato scope: a
	// later change that adds an unauthenticated path fails here.
	it('is registered at potato scope', () => {
		expect(ROUTE_SCOPES.get('PUT /mirror/:prefix/:hash')).toBe('potato');
	});

	// The key builder and the validator, without an HTTP round trip: this is where
	// traversal is tested, because a URL carrying it never gets this far.
	it('refuses anything that is not a content address', () => {
		expect(validMirrorKey(`${PREFIX}/${HASH}`)).toBe(true);
		expect(mirrorKeyFor(`${PREFIX}/${HASH}`)).toBe(`media/${PREFIX}/${HASH}`);
		const bad = [
			'', // empty
			HASH, // no fan-out
			`${PREFIX}/`, // no hash
			`/ ${HASH}`, // a space
			`${PREFIX}/${HASH}/`, // trailing separator
			`../${HASH}`, // traversal in the fan-out
			`${PREFIX}/../../etc/passwd`, // traversal in the hash
			`${PREFIX}/${HASH.toUpperCase()}`, // uppercase
			`mirror/${PREFIX}/${HASH}`, // already prefixed
		];
		for (const key of bad) {
			expect(validMirrorKey(key), key).toBe(false);
		}
	});
});

// The defect review found: nothing compared the body with its address, so a
// corrupt upload was stored under a valid key and a later PUT replaced it, which
// makes the PUT wrong rather than idempotent. R2 now hashes what it receives.
describe('the bytes must be the bytes the address names', () => {
	// Each of these owns an address no other test writes, because the bucket is
	// shared across the file.
	it('refuses a body that does not hash to its address, and stores nothing', async () => {
		await seedTokens();
		const address = sha256('the body that belongs at this address');
		const prefix = address.slice(0, 2);
		const { res, json } = await mirror(prefix, address, {}, TOKENS.potato, 'not the body');
		expect(res.status).toBe(400);
		expect(json<{ error: string }>().error).toBe('content does not match the address');
		expect(await (env as unknown as Env).BUCKET.head(keyOf(address))).toBeNull();
	});

	// The consequence the finding described: a second PUT of different bytes used
	// to overwrite the first, so a "retry" could corrupt a good object.
	it('cannot be overwritten by different bytes at the same address', async () => {
		await seedTokens();
		const good = 'the bytes that belong here';
		const address = sha256(good);
		const prefix = address.slice(0, 2);

		const first = await mirror(prefix, address, {}, TOKENS.potato, good);
		expect(first.res.status).toBe(201);
		const second = await mirror(prefix, address, {}, TOKENS.potato, 'different bytes, same address');
		expect(second.res.status).toBe(400);

		const stored = await (env as unknown as Env).BUCKET.get(keyOf(address));
		const bytes = await stored!.text();
		expect(bytes).toBe(good);
		// And the object's metadata still names the bytes that are actually there.
		expect(stored!.customMetadata?.sha256).toBe(address);
		expect(sha256(bytes)).toBe(address);
	});

	it('recognises the runtime checksum failure it maps to 400', () => {
		expect(
			isChecksumMismatch(
				new Error('put: The SHA-256 checksum you specified did not match what we received.'),
			),
		).toBe(true);
		expect(isChecksumMismatch(new Error('network connection lost'))).toBe(false);
	});
});
