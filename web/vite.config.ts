import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig, loadEnv } from 'vite';

// One static SPA, served by the Go binary that embeds web/build. No SSR, no
// Node at runtime, and no asset origin: the shell, its assets and the API share
// one process and one identity lock (design.md §3).
export default defineConfig(({ mode }) => ({
	plugins: [
		sveltekit({
			compilerOptions: {
				// Runes everywhere in this project, including the shell.
				runes: ({ filename }) => (filename.split(/[/\\]/).includes('node_modules') ? undefined : true)
			},
			adapter: adapter({
				pages: 'build',
				assets: 'build',
				fallback: 'index.html',
				precompress: false,
				strict: false
			})
		})
	],
	// Kit 3's postbuild analysis imports the server bundle in Node, where the
	// CommonJS `cookie` package has no named exports. Bundling it into the SSR
	// output is the fix; it affects the build machine only, never the image.
	ssr: {
		noExternal: ['cookie']
	},
	server: {
		// `make dev` runs Go on 8080; `pnpm dev` proxies the API to it so the
		// shell and the handlers can be worked on together. HARBOR_API points it
		// elsewhere when 8080 is taken (two worktrees, two servers). loadEnv reads
		// the process environment, so this needs no Node types.
		proxy: {
			'/api': loadEnv(mode, '.', 'HARBOR_').HARBOR_API ?? 'http://127.0.0.1:8080'
		}
	}
}));
