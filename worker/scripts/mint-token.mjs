#!/usr/bin/env node
// Mint a door (or the potato) token without ever storing it.
//
//   node scripts/mint-token.mjs shortcut door
//   node scripts/mint-token.mjs potato potato
//
// It prints the token once and the SQL to insert its hash. The token is not
// written anywhere else: put it in the caller's config (the iOS Shortcut, the
// Telegram bot setup, the potato's .env) and lose this terminal.
//
//   wrangler d1 execute harbor --remote --command "<the INSERT it prints>"
import { createHash, randomBytes } from 'node:crypto';

const [id, scope = 'door'] = process.argv.slice(2);
// The token id *is* the job's `source`, and `source` is an enum in
// contracts/job.schema.json. A door minted under another name would write jobs
// the potato rejects, so the name is checked here rather than discovered later.
const DOORS = ['telegram', 'shortcut'];
const valid = (scope === 'door' && DOORS.includes(id)) || (scope === 'potato' && id === 'potato');
if (!valid) {
	console.error(`usage: mint-token.mjs <${DOORS.join('|')}> door   |   mint-token.mjs potato potato`);
	process.exit(2);
}

const token = randomBytes(32).toString('base64url');
const sha256 = createHash('sha256').update(token).digest('hex');
const now = Math.floor(Date.now() / 1000);

console.log(`# ${id} (${scope}) — store this token now, it is not recoverable:`);
console.log(token);
console.log('');
console.log('# insert it (the hash, not the token) with:');
console.log(
	`wrangler d1 execute harbor --remote --command "INSERT INTO tokens (id, scope, sha256, label, created_at) VALUES ('${id}', '${scope}', '${sha256}', '${id}', ${now}) ON CONFLICT(id) DO UPDATE SET sha256=excluded.sha256, scope=excluded.scope, revoked_at=NULL"`,
);
