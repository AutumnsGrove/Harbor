#!/usr/bin/env node
// SvelteKit emits one inline bootstrap script per page. Go serves the strict CSP,
// so the hashes have to be computed from the built output and handed to the
// binary: `script-src 'self' 'sha256-…'`, never `unsafe-inline`.
//
// Runs after `vite build` and writes build/csp.json. The Go server validates
// every hash against the CSP base64 grammar before it reaches a header, so a
// mangled file degrades to "no inline script allowed", not to a hole.

import { createHash } from 'node:crypto';
import { readFile, readdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const buildDir = fileURLToPath(new URL('../build/', import.meta.url));

async function htmlFiles(dir) {
	const entries = await readdir(dir, { withFileTypes: true });
	const files = [];
	for (const entry of entries) {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) {
			files.push(...(await htmlFiles(path)));
		} else if (entry.name.endsWith('.html')) {
			files.push(path);
		}
	}
	return files;
}

// <script> without src: the browser hashes the exact body text, so nothing may
// be trimmed or re-encoded here.
const inlineScript = /<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/g;

const hashes = new Set();
for (const file of await htmlFiles(buildDir)) {
	const html = await readFile(file, 'utf8');
	for (const match of html.matchAll(inlineScript)) {
		const body = match[1] ?? '';
		if (body.trim() === '') continue;
		hashes.add('sha256-' + createHash('sha256').update(body, 'utf8').digest('base64'));
	}
}

const sorted = [...hashes].sort();
await writeFile(join(buildDir, 'csp.json'), JSON.stringify({ scriptHashes: sorted }, null, '\t') + '\n');

console.log(`csp: ${sorted.length} inline script hash(es) → build/csp.json`);
