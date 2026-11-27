import { MAX_UPLOAD_BYTES } from './constants';
import type { Env } from './types';

// Mirror-up (design.md §11, plans/06-media.md): the potato holds the original of
// everything saved in the app, and this is how a second copy of it reaches R2.
//
// The key is the content address, so the operation is naturally idempotent: two
// uploads of the same bytes are one object, and a retry after a flaky connection
// costs one PUT. That is why there is no list, no delete and no read: the mirror
// exists to survive the potato, not to be managed.
//
// Like backups, nothing here can read an object back out. A restore pulls with a
// presigned GET through `/presign`, so the Worker never becomes a read surface
// for the library.

const PREFIX = 'media/';

/** `media/<two hex>/<64 hex>`: the fan-out and the hash, and nothing else. */
const KEY = /^[0-9a-f]{2}\/[0-9a-f]{64}$/;

export interface MirrorSummary {
	key: string;
	bytes: number;
}

/**
 * The body did not hash to the address it was uploaded under.
 *
 * The key alone cannot make this PUT idempotent: two different bodies at one
 * key are two writes and the second wins. So the address is handed to R2 as a
 * checksum and R2 refuses a mismatch — which is why this is its own class. A
 * corrupt upload is the caller's own disagreement with itself, and it must not
 * read as a 502 the potato would retry forever.
 */
export class ChecksumMismatch extends Error {
	constructor(readonly address: string) {
		super(`body does not match the content address ${address}`);
		this.name = 'ChecksumMismatch';
	}
}

/**
 * R2's checksum failure, recognised by its message. Narrow string matching is
 * not lovely, but the alternative is buffering up to 25 MB to hash it here in a
 * Worker, and R2 is the party that must refuse the write in any case.
 */
export function isChecksumMismatch(err: unknown): boolean {
	return /checksum/i.test(String(err));
}

/** The R2 key for a content address, "ab/abcd…" → "media/ab/abcd…". */
export function mirrorKeyFor(path: string): string {
	return PREFIX + path;
}

/**
 * A content address becomes an R2 key, so it is validated as one. Anything else
 * — a traversal, a filename, an uppercase hash — is refused rather than
 * normalised: the potato derives the key from the bytes it hashed, so a key that
 * is not that shape is a bug worth surfacing.
 */
export function validMirrorKey(hash: string): boolean {
	return KEY.test(hash);
}

/**
 * Writes one blob. The prefix must agree with the hash — a mismatch would file
 * an object under a key whose fan-out disagrees with its own name, which no
 * later sweep could repair — and that is checked here rather than trusted.
 */
export async function putMirror(
	env: Env,
	prefix: string,
	hash: string,
	body: ReadableStream,
	meta: { contentType: string },
): Promise<MirrorSummary> {
	if (!validMirrorKey(`${prefix}/${hash}`) || prefix !== hash.slice(0, 2)) {
		throw new ReferenceError(`${prefix}/${hash}`);
	}
	const key = mirrorKeyFor(`${prefix}/${hash}`);
	let object: R2Object;
	try {
		object = await env.BUCKET.put(key, body, {
			httpMetadata: { contentType: meta.contentType },
			// The address is the checksum: R2 hashes what it receives and refuses
			// a mismatch, so a truncated or corrupt upload is a refusal rather
			// than a permanently wrong object filed under a valid address (found
			// in review — the key alone could not promise that).
			sha256: hash,
			// Repeating the hash in metadata is not redundant bookkeeping: it is
			// what a later integrity check reads.
			customMetadata: { sha256: hash },
		});
	} catch (err) {
		if (isChecksumMismatch(err)) throw new ChecksumMismatch(`${prefix}/${hash}`);
		throw err;
	}
	return { key, bytes: object.size };
}
