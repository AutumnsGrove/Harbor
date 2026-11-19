import path from 'node:path';
import { cloudflareTest, readD1Migrations } from '@cloudflare/vitest-pool-workers';
import { defineConfig } from 'vitest/config';

// The tests run in workerd with real D1 and R2 (miniflare), not mocks: the point
// of testing the queue is the SQL, and a mock cannot tell you whether
// UPDATE … RETURNING is atomic. Migrations are applied per test file.
export default defineConfig(async () => {
	const migrations = await readD1Migrations(path.join(import.meta.dirname, 'migrations'));
	return {
		plugins: [
			cloudflareTest({
				wrangler: { configPath: './wrangler.toml' },
				miniflare: { bindings: { TEST_MIGRATIONS: migrations, VERSION: 'test', DOOR_LIMIT_MAX: '100000' } },
			}),
		],
		test: {
			setupFiles: ['./test/setup.ts'],
		},
	};
});
