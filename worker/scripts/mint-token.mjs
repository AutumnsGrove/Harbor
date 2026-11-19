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
if (!id || !['door', 'potato'].includes(scope)) {
	console.error('usage: mint-token.mjs <id> [door|potato]');
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
