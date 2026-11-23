<script lang="ts">
	// Home: the library grid, the composer, and the first-run state. Phase 2's
	// feed is deliberately plain — no Spaces, tags or search rails until their
	// phases land; what is here is real data from /api/items.
	import { onMount } from 'svelte';

	import { api, isApiError, type Item, type PendingJob } from '#lib/api';

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
	let pending = $state<PendingJob[]>([]);
	let pendingProblem = $state<string | null>(null);
	let lastPending = new Set<string>();
	let failed = $state<Item[]>([]);
	let attentionDismissed = $state(false);
	let retrying = $state<string | null>(null);

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

	async function loadAttention() {
		try {
			const answer = await api.attention.list();
			failed = answer.items;
		} catch {
			// The library itself is fine; the strip is an optional warning.
		}
	}

	async function retry(item: Item) {
		retrying = item.id;
		try {
			await api.items.retry(item.id);
			failed = failed.filter((entry) => entry.id !== item.id);
			say('Retrying. It will leave the queue in a minute.');
			void loadPending();
		} catch (error) {
			problem = message(error);
		} finally {
			retrying = null;
		}
	}

	function saved(item: Item, merged: boolean) {
		composing = false;
		say(merged ? `Merged into “${item.title || item.url}”` : 'Saved.');
		void load();
		void loadPending();
	}

	// The pending window follows the 30 s loop, but only while the tab is
	// visible. A job that disappears from the list has become an item, which is
	// the moment to reload the feed (design.md §5).
	async function loadPending() {
		try {
			const answer = await api.pending.list();
			const ids = new Set(answer.jobs.map((job) => job.id));
			const finished = [...lastPending].some((id) => !ids.has(id));
			pending = answer.jobs;
			pendingProblem = null;
			lastPending = ids;
			if (finished) await load();
		} catch (error) {
			pendingProblem = isApiError(error) ? error.message : 'The Worker is not answering.';
		}
	}

	function pendingTitle(job: PendingJob): string {
		if (!job.url) return job.title;
		try {
			return job.title === job.url ? new URL(job.url).hostname.replace(/^www\./, '') : job.title;
		} catch {
			return job.title;
		}
	}

	function pendingAge(iso: string): string {
		const seconds = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
		if (seconds < 60) return 'now';
		if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
		return `${Math.round(seconds / 3600)}h`;
	}

	onMount(() => {
		void load();
		void loadPending();
		void loadAttention();
		const timer = setInterval(() => {
			if (document.visibilityState === 'visible') {
				void loadPending();
				void loadAttention();
			}
		}, 30_000);
		return () => clearInterval(timer);
	});
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
{:else if items.length === 0 && pending.length === 0}
	<!-- The real first-run screen from the mockup. Both paths are live now: import
	     is phase 5's, and the composer works, so neither is a promise. -->
	<header class="pagehead">
		<h1 class="h1">Bring something in.</h1>
		<p class="lead">
			Your library is empty. Import the old one, or save a single link and watch it appear.
		</p>
	</header>

	<div class="firstrun">
		<section class="path">
			<span class="pill acc badge">ready</span>
			<h2 class="h3">Import your Raindrop library</h2>
			<p>
				CSV and Netscape HTML, merged by canonical link, so importing twice is safe. Collections become
				Spaces, and your highlights come with them.
			</p>
			<a class="btn pri" href="/settings/import"><Icon name="plus" /> Import a file</a>
		</section>

		<section class="path">
			<span class="pill acc badge">ready</span>
			<h2 class="h3">Paste a first link</h2>
			<p>Saved in one motion. A note is enough, too — the composer takes either.</p>
			<button class="btn" onclick={() => (composing = true)}>
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

	{#if failed.length > 0 && !attentionDismissed}
		<div class="attention" role="status">
			<div class="attention-head">
				<Icon name="alert" size={15} />
				<strong
					>{failed.length} save{failed.length === 1 ? '' : 's'} couldn't be read.</strong
				>
				<span class="dim">The save is safe; the enrichment is not.</span>
				<button class="btn sm ghost" onclick={() => (attentionDismissed = true)}>
					Not now
				</button>
			</div>
			{#each failed.slice(0, 3) as entry (entry.id)}
				<div class="attention-row">
					<span class="grow">
						{entry.title || entry.url || 'Untitled note'}
						{#if entry.lastError}<span class="dim"> · {entry.lastError}</span>{/if}
					</span>
					<button class="btn sm" onclick={() => retry(entry)} disabled={retrying === entry.id}>
						{retrying === entry.id ? 'Retrying…' : 'Retry'}
					</button>
				</div>
			{/each}
		</div>
	{/if}

	{#if pendingProblem}
		<p class="pending-note" role="status">
			<Icon name="alert" size={14} /> Pending saves are unavailable right now. The library itself is fine.
		</p>
	{/if}

	<div class="feed">
		{#each pending as job (job.id)}
			<article class="card pending" aria-label={`${job.title} — saving`}>
				<div class="cover">
					<span class="kindmark"><Icon name="clock" size={20} /></span>
				</div>
				<div class="body">
					<h3>{pendingTitle(job)}</h3>
					<p class="sub"><span>{job.source}</span><span>· {pendingAge(job.createdAt)}</span></p>
					<p class="more">Waiting for the potato to pull this save.</p>
				</div>
			</article>
		{/each}
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
