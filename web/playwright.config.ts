import { defineConfig, devices } from '@playwright/test';

// #58: the geometry assertions jsdom cannot make. The tests serve the *built*
// SPA — the same web/build the Go binary embeds — because the feed's order is a
// property of the shipped shell, not of a dev server.
export default defineConfig({
	testDir: './e2e',
	// One browser is enough: the assertion is layout, and Chromium is what the
	// review on #57 measured by hand.
	projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
	fullyParallel: false,
	forbidOnly: !!process.env.CI,
	retries: process.env.CI ? 1 : 0,
	reporter: process.env.CI ? 'line' : 'list',
	use: {
		baseURL: 'http://127.0.0.1:4173',
		trace: 'retain-on-failure'
	},
	webServer: {
		// `vite preview` serves web/build, so the test never needs the Go binary.
		// --host pins 127.0.0.1: `vite preview` otherwise binds ::1 on macOS and the
		// readiness probe against 127.0.0.1 waits out the timeout.
		command: 'pnpm build && pnpm preview --host 127.0.0.1 --port 4173 --strictPort',
		url: 'http://127.0.0.1:4173',
		reuseExistingServer: !process.env.CI,
		timeout: 120_000
	}
});
