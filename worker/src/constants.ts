// Caps as constants, so a test can cite them and a reviewer can see them in one
// place. Design.md §5 and §11 are where the numbers come from.

export const MAX_UPLOAD_BYTES = 25 * 1024 * 1024;
export const MAX_SAVE_BODY_BYTES = 96 * 1024;
export const MAX_NOTE_CHARS = 65536;
export const MAX_LINK_CHARS = 2048;

/** Ten minutes: a potato that dies mid-job must not strand a save. */
export const LEASE_SECONDS = 600;
/** How long a Telegram album is held so its later files land in the same job. */
export const ALBUM_HOLD_SECONDS = 10;
/** After this many claims the job stops being served and becomes visible as failed. */
export const MAX_ATTEMPTS = 4;
/** Orphaned `incoming/` objects are deleted after this long. */
export const ORPHAN_AGE_SECONDS = 7 * 24 * 60 * 60;
/** Presigned GETs are short-lived on purpose (design.md §11). */
export const PRESIGN_TTL_SECONDS = 300;

export const CONTENT_TYPE_ALLOWLIST = [
	/^image\/[a-z0-9][a-z0-9.+-]*$/i,
	/^application\/pdf$/i,
	/^text\/plain$/i,
	/^text\/markdown$/i,
];

export function contentAllowed(type: string): boolean {
	return CONTENT_TYPE_ALLOWLIST.some((re) => re.test(type));
}
