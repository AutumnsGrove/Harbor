<script lang="ts">
	// Home: the library grid, the composer, and the first-run state. Phase 2's
	// feed is deliberately plain — no Spaces, tags or search rails until their
	// phases land; what is here is real data from /api/items.
	import { onMount } from 'svelte';

	import { api, isApiError, type Item } from '#lib/api';

	import Card from '../lib/components/Card.svelte';
	import Composer from '../lib/components/Composer.svelte';
	import Icon from '../lib/components/Icon.svelte';

	let items = $state<Item[]>([]);
	let next = $state<string | null>(null);
	let loading = $state(true);
	let loadingMore = $state(false);
	let problem = $state<string | null>(null);
	let composing = $state(false);
	let toast = $state<string | null>(null);

	let toastTimer: ReturnType<typeof setTimeout> | undefined;

	function say(message: string) {
		toast = message;
		clearTimeout(toastTimer);
		toastTimer = setTimeout(() => (toast = null), 4000);
	}

	function message(error: unknown): string {
		if (isApiError(error)) {
			if (error.status === 403) {
				return 'Not signed in through Tailscale. Open Harbor from its tailnet address.';
			}
			return error.message;
		}
		return error instanceof Error ? error.message : 'Could not read the library.';
	}

	async function load() {
		problem = null;
		try {
			const page = await api.items.list({ limit: 30 });
			items = page.items;
			next = page.next;
		} catch (error) {
			problem = message(error);
		} finally {
			loading = false;
		}
	}

	async function loadMore() {
		if (!next || loadingMore) return;
		loadingMore = true;
		try {
			const page = await api.items.list({ limit: 30, cursor: next });
			items = [...items, ...page.items];
			next = page.next;
		} catch (error) {
			problem = message(error);
		} finally {
			loadingMore = false;
		}
	}

	function saved(item: Item, merged: boolean) {
		composing = false;
		say(merged ? `Merged into “${item.title || item.url}”` : 'Saved.');
		void load();
	}

	onMount(load);
</script>

<svelte:head>
	<title>Harbor</title>
</svelte:head>

{#if loading}
	<div class="empty"><p class="muted">Reading the library…</p></div>
{:else if problem && items.length === 0}
	<div class="empty">
		<h3>The library did not answer.</h3>
		<p>{problem}</p>
		<button class="btn" onclick={load}><Icon name="refresh" /> Try again</button>
	</div>
{:else if items.length === 0}
	<!-- The real first-run screen from the mockup. Import is a stub until
	     phase 5; the composer works now, so it is a button, not a promise. -->
	<header class="pagehead">
		<h1 class="h1">Bring something in.</h1>
		<p class="lead">
			Your library is empty. Import the old one, or save a single link and watch it appear.
		</p>
	</header>

	<div class="firstrun">
		<section class="path">
			<span class="pill amb badge">not yet</span>
			<h2 class="h3">Import your Raindrop library</h2>
			<p>
				CSV and Netscape HTML, merged by canonical link, so importing twice is safe. Lands with
				phase&nbsp;5.
			</p>
		</section>

		<section class="path">
			<span class="pill acc badge">ready</span>
			<h2 class="h3">Paste a first link</h2>
			<p>Saved in one motion. A note is enough, too — the composer takes either.</p>
			<button class="btn pri" onclick={() => (composing = true)}>
				<Icon name="plus" /> Save something
			</button>
		</section>
	</div>

	<aside class="callout firstrun-note">
		<span>
			Telegram and the share sheet arrive with phase&nbsp;4; they write the same save this composer
			does.
		</span>
	</aside>
{:else}
	<div class="toolbar">
		<h1 class="h2">Library</h1>
		<span class="dim num"
			>{items.length}{next ? '+' : ''} item{items.length === 1 ? '' : 's'}</span
		>
		<span class="sp"></span>
		<button class="btn pri" onclick={() => (composing = true)}><Icon name="plus" /> Save</button>
	</div>

	{#if problem}
		<div class="callout amb banner" role="status">
			<Icon name="alert" size={16} />
			<span>{problem}</span>
			<button class="btn sm ghost" onclick={load}>Retry</button>
		</div>
	{/if}

	<div class="feed">
		{#each items as item (item.id)}
			<Card {item} />
		{/each}
	</div>

	{#if next}
		<div class="more-row">
			<button class="btn" onclick={loadMore} disabled={loadingMore}>
				{loadingMore ? 'Loading…' : 'Load more'}
			</button>
		</div>
	{/if}
{/if}

{#if composing}
	<Composer onclose={() => (composing = false)} onsaved={saved} />
{/if}

{#if toast}
	<div class="toast" role="status">{toast}</div>
{/if}
