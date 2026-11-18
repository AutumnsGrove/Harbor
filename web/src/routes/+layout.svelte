<script lang="ts">
	import '../app.css';

	import { page } from '$app/state';

	let { children } = $props();

	const onSettings = $derived(page.url.pathname.startsWith('/settings'));
</script>

<div class="app">
	<header class="top">
		<a class="brand" href="/">Harbor<em>.</em></a>

		<!-- Search arrives with phase 10. It is disabled, not decorative: a field
		     that silently does nothing is worse than one that says so. -->
		<div class="searchbox">
			<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" aria-hidden="true">
				<circle cx="11" cy="11" r="7" />
				<path d="m20 20-3.5-3.5" />
			</svg>
			<input type="search" placeholder="Search the library" aria-label="Search the library" disabled />
		</div>

		<nav class="nav" aria-label="Main">
			<a href="/" aria-current={onSettings ? undefined : 'page'}>Library</a>
			<a href="/settings" aria-current={onSettings ? 'page' : undefined}>Settings</a>
		</nav>
	</header>

	<main class="page">
		{@render children()}
	</main>
</div>
