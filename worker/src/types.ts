// The shapes both the Worker and the potato agree on. The JSON Schema in
// contracts/ is the cross-language version of the same thing; a field change
// lands with a fixture in both places.

export type Source = 'telegram' | 'shortcut';
export type Kind = 'link' | 'note' | 'upload';
export type Status = 'queued' | 'claimed' | 'done' | 'failed';
export type Scope = 'door' | 'potato';

// The bindings live in the global `Cloudflare.Env` namespace, which is also what
// `cloudflare:test` hands the tests: one declaration, so a test cannot validate
// against a different environment than the deployed Worker sees.
declare global {
	namespace Cloudflare {
		interface Env {
			DB: D1Database;
			BUCKET: R2Bucket;
			/** Snapshots, not intake: a separate bucket so its retention and its
			 *  privacy are not tangled with media (design.md section 11). */
			BACKUPS: R2Bucket;
			/** Per-IP dampening; absent in tests and in a binding-less deploy (src/limits.ts). */
			DOOR_LIMIT?: RateLimit;
			R2_BUCKET: string;
			R2_BACKUPS_BUCKET?: string;
			VERSION: string;
			/** Override for the door dampener's window limit (default 30, see limits.ts). */
			DOOR_LIMIT_MAX?: string;
			/** Ops override for the orphan window; defaults to seven days in constants.ts. */
			ORPHAN_AGE_SECONDS?: string;
			R2_ACCOUNT_ID?: string;
			R2_ACCESS_KEY_ID?: string;
			R2_SECRET_ACCESS_KEY?: string;
		}
	}
}

export type Env = Cloudflare.Env;

export interface JobRow {
	id: string;
	source: Source;
	kind: Kind;
	link: string | null;
	note: string | null;
	title: string | null;
	payload_json: string;
	group_key: string | null;
	touched_at: number;
	status: Status;
	attempts: number;
	claimed_at: number | null;
	lease_until: number | null;
	last_error: string;
	created_at: number;
	updated_at: number;
	done_at: number | null;
}

export interface BlobRow {
	job_id: string;
	n: number;
	r2_key: string;
	content_type: string;
	bytes: number;
	created_at: number;
}

/** One job as the potato sees it: the row plus its blobs (contracts/job.schema.json). */
export interface JobEnvelope {
	id: string;
	source: Source;
	kind: Kind;
	link: string | null;
	note: string | null;
	title: string | null;
	group_key: string | null;
	created_at: number;
	status: Status;
	attempts: number;
	last_error: string;
	blobs: { n: number; key: string; content_type: string; bytes: number }[];
}

export interface Principal {
	id: string;
	scope: Scope;
}
