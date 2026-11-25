import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vitest/config';

// Two kinds of test live here. The API client and the pure rules are plain
// TypeScript and run in node, unchanged. Component tests (a Card with a cover
// and one without) need the Svelte compiler and a DOM, so they opt in with a
// `@vitest-environment jsdom` docblock rather than making every test pay for a
// browser-shaped global.
export default defineConfig({
	plugins: [sveltekit()],
	// Without the browser condition Svelte resolves its server build and every
	// component test dies on `mount(...)` being unavailable outside a component.
	resolve: { conditions: ['browser'] },
	test: {
		include: ['src/**/*.test.ts'],
		environment: 'node'
	}
});
