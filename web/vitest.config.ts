import { defineConfig } from 'vitest/config';

// The API client and the pure rules are plain TypeScript, so the tests do not
// need the Svelte compiler: this config is deliberately separate from the build.
export default defineConfig({
	test: {
		include: ['src/**/*.test.ts'],
		environment: 'node'
	}
});
