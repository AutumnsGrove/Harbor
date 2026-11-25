import { AwsClient } from 'aws4fetch';
import { validName } from './backups';
import { PRESIGN_TTL_SECONDS } from './constants';
import type { Env } from './types';

// A short-lived, S3-style GET for one known key. The browser fetches the bytes
// straight from R2: they never pass through the Worker (a Worker-served signed
// URL would be a public read path, design.md §11) and the potato needs no R2
// credentials for ordinary reads.
//
// The credential is a read-only R2 API token, held as a Worker secret, scoped to
// this bucket. It is not in D1 and never leaves the Worker.

export interface PresignResult {
	url: string;
	expires_at: number;
}

export function presignConfigured(env: Env): boolean {
	return Boolean(env.R2_ACCOUNT_ID && env.R2_ACCESS_KEY_ID && env.R2_SECRET_ACCESS_KEY);
}

/**
 * A key is either a job blob, which D1 has to prove, or a backup, which its own
 * name proves: the bucket holds snapshots the database never saw and must not
 * need a row to be restorable (issue #60).
 */
export type KeyScope = 'job' | 'backup';

/** Only the prefixes this Worker writes, and only keys that look like them. */
export function presignKeyScope(key: string): KeyScope | null {
	if (!key || key.length > 512) return null;
	if (key.includes('..') || key.startsWith('/')) return null;
	if (key.startsWith('backups/')) {
		return validName(key.slice('backups/'.length)) ? 'backup' : null;
	}
	return /^(incoming|media|archives)\/[A-Za-z0-9._\-/]+$/.test(key) ? 'job' : null;
}

export async function presign(env: Env, key: string, now: number): Promise<PresignResult> {
	if (!presignConfigured(env)) throw new Error('presign is not configured');
	const client = new AwsClient({
		accessKeyId: env.R2_ACCESS_KEY_ID as string,
		secretAccessKey: env.R2_SECRET_ACCESS_KEY as string,
		service: 's3',
		region: 'auto',
	});

	const encoded = key.split('/').map(encodeURIComponent).join('/');
	const url = new URL(`https://${env.R2_ACCOUNT_ID}.r2.cloudflarestorage.com/${env.R2_BUCKET}/${encoded}`);
	url.searchParams.set('X-Amz-Expires', String(PRESIGN_TTL_SECONDS));

	// aws4fetch signs the query rather than the headers, which is what makes the
	// URL itself the capability.
	const signed = await client.sign(url.toString(), { method: 'GET', aws: { signQuery: true } });
	return { url: signed.url.toString(), expires_at: now + PRESIGN_TTL_SECONDS };
}
