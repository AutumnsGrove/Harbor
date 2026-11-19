import type { D1Migration } from '@cloudflare/vitest-pool-workers';

// Test-only bindings on top of the Worker's real environment.
declare global {
	namespace Cloudflare {
		interface Env {
			TEST_MIGRATIONS: D1Migration[];
		}
	}
}
