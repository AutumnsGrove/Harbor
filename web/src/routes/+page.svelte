<script lang="ts">
	// Phase 0 is the shell only. The empty state is the real first-run screen from
	// the mockup, with both paths marked as not built yet rather than faked
	// (design.md §9: import or paste a first link).
</script>

<svelte:head>
	<title>Harbor</title>
</svelte:head>

<header class="pagehead">
	<h1 class="h1">A library that files itself.</h1>
	<p class="lead">
		Links, notes, images and quotes, saved in one motion. Nothing here yet — the shell is live, the
		library arrives with the next phases.
	</p>
</header>

<div class="firstrun">
	<section class="path">
		<span class="pill amb badge">not yet</span>
		<h2 class="h2">Import your Raindrop library</h2>
		<p>
			CSV and Netscape HTML, merged by canonical link, so importing twice is safe. Lands with phase&nbsp;5.
		</p>
	</section>

	<section class="path">
		<span class="pill amb badge">not yet</span>
		<h2 class="h2">Paste a first link</h2>
		<p>
			The composer, the Telegram bot and the phone gesture all write the same save. Lands with
			phases&nbsp;2–4.
		</p>
	</section>
</div>

<aside class="callout firstrun-note">
	<span>
		This is the phase-0 binary: a version stamp, a health endpoint, the identity lock and the update
		flow. <a href="/settings">Settings</a> shows what is actually running.
	</span>
</aside>
