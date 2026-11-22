import { MAX_BACKUP_BYTES } from './constants';
import type { Env } from './types';

// Snapshot upload (design.md §4: `PUT /backups/<name>` + list + delete, potato
// scope). The potato holds no R2 credential, so this is how the copy that
// survives the potato gets off it.
//
// The bucket is private and nothing here can read a backup back out: the restore
// path pulls with a presigned GET, the same way media does, so the Worker never
// becomes a read surface for the library.

const PREFIX = 'backups/';

/** A backup's name becomes an R2 key, so it is validated as one. */
const NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,120}\.db\.zst$/;

export interface BackupSummary {
	name: string;
	bytes: number;
	uploaded: number;
}

export function validName(name: string): boolean {
	return NAME.test(name);
}

export function backupKey(name: string): string {
	return PREFIX + name;
}

export async function putBackup(
	env: Env,
	name: string,
	body: ReadableStream,
	meta: { sha256: string; schema: number },
): Promise<BackupSummary> {
	if (!validName(name)) throw new NameError(name);
	const object = await env.BACKUPS.put(backupKey(name), body, {
		httpMetadata: { contentType: 'application/zstd' },
		customMetadata: { sha256: meta.sha256, schema: String(meta.schema) },
	});
	return { name, bytes: object.size, uploaded: Math.floor(Date.now() / 1000) };
}

export async function listBackups(env: Env): Promise<BackupSummary[]> {
	const listed = await env.BACKUPS.list({ prefix: PREFIX });
	return listed.objects
		.map((object) => ({
			name: object.key.slice(PREFIX.length),
			bytes: object.size,
			uploaded: Math.floor(new Date(object.uploaded).getTime() / 1000),
		}))
		.sort((a, b) => (a.name < b.name ? 1 : -1));
}

export async function deleteBackup(env: Env, name: string): Promise<boolean> {
	if (!validName(name)) throw new NameError(name);
	const key = backupKey(name);
	const existing = await env.BACKUPS.head(key);
	if (!existing) return false;
	await env.BACKUPS.delete(key);
	return true;
}

/** A name that is not a backup's. The route turns it into a 400. */
export class NameError extends Error {
	constructor(name: string) {
		super(`not a backup name: ${name}`);
		this.name = 'NameError';
	}
}

export const BACKUP_LIMIT_BYTES = MAX_BACKUP_BYTES;
