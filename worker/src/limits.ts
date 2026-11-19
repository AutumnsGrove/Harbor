import type { Env } from './types';

// Per-caller dampening on the two public doors: abuse-dampening, not a hard
// guarantee (design.md §11).
//
// The two platform answers both turned out useless on this account, so the
// counter is ours:
//
//   - the Workers rate-limit binding always answers `success: true` on live
//     Workers (workers-sdk#16097, reproduced here on 2026-11-19 with a
//     2-request/10-second limit);
//   - the Cache API is a no-op on workers.dev, so a counter kept there never
//     trips either.
//
// So: one D1 row per (caller, window), incremented with an UPSERT. It costs one
// write per save, which is nothing at this scale and is worth it for a door that
// faces the internet. The write is best-effort — if it fails, the save is still
// accepted, because the save path must never fail for bookkeeping.

export const DOOR_LIMIT_DEFAULT = 30;
export const DOOR_LIMIT_PERIOD = 60;

export interface LimitOutcome {
	allowed: boolean;
	retryAfter: number;
}

/** The key is the door plus the caller, so one noisy source cannot starve another. */
export function doorLimitKey(source: string, req: Request): string {
	const ip = req.headers.get('cf-connecting-ip') ?? 'unknown';
	return `${source}:${ip}`;
}

export interface DoorLimitOptions {
	now?: number;
	limit?: number;
}

export async function doorLimit(env: Env, key: string, opts: DoorLimitOptions = {}): Promise<LimitOutcome> {
	const now = opts.now ?? Date.now();
	const limit = opts.limit ?? Number(env.DOOR_LIMIT_MAX ?? DOOR_LIMIT_DEFAULT);
	const window = Math.floor(now / 1000 / DOOR_LIMIT_PERIOD);

	// The binding, when Cloudflare fixes it: cheapest possible rejection.
	if (env.DOOR_LIMIT) {
		try {
			const { success } = await env.DOOR_LIMIT.limit({ key });
			if (!success) return { allowed: false, retryAfter: DOOR_LIMIT_PERIOD };
		} catch {
			// fall through to the counter
		}
	}

	try {
		const row = await env.DB.prepare(
			`INSERT INTO door_limits (key, window, hits, updated_at) VALUES (?, ?, 1, ?)
			 ON CONFLICT(key, window) DO UPDATE SET hits = hits + 1, updated_at = excluded.updated_at
			 RETURNING hits`,
		)
			.bind(key, window, Math.floor(now / 1000))
			.first<{ hits: number }>();
		const hits = Number(row?.hits ?? 0);
		if (hits > limit) {
			const resetAt = (window + 1) * DOOR_LIMIT_PERIOD;
			return { allowed: false, retryAfter: Math.max(1, resetAt - Math.floor(now / 1000)) };
		}
	} catch {
		// Counting failed: the save still goes through. Losing a save to a
		// bookkeeping error is the one thing this system must never do.
	}

	return { allowed: true, retryAfter: 0 };
}

/** Windows older than a day are dead rows; the daily sweep clears them. */
export async function pruneDoorLimits(env: Env, now: number): Promise<number> {
	const cutoff = Math.floor(now / 1000 / DOOR_LIMIT_PERIOD) - 24 * 60;
	const res = await env.DB.prepare('DELETE FROM door_limits WHERE window < ?').bind(cutoff).run();
	return res.meta.changes ?? 0;
}
