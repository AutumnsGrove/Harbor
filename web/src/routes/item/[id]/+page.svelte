<script lang="ts">
	// The item page: title, the kind picker, the model's summary, a per-kind panel
	// built on details_json, the owner's note, tags and provenance. The reader and
	// highlights arrive with phase 8 and media with phase 6; nothing here pretends
	// otherwise.
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { onMount } from 'svelte';

	import { api, isApiError, type Item } from '#lib/api';
	import { kindIcons, kinds } from '#lib/kinds';

	import Icon from '../../../lib/components/Icon.svelte';
	import KindPanel from '../../../lib/components/KindPanel.svelte';
	import SummaryBlock from '../../../lib/components/SummaryBlock.svelte';
	import TagRow from '../../../lib/components/TagRow.svelte';

	const id = $derived(page.params.id ?? '');

	let item = $state<Item | null>(null);
	let title = $state('');
	let note = $state('');
	let kind = $state<Item['kind']>('article');
	let loading = $state(true);
	let saving = $state(false);
	let confirmingDelete = $state(false);
	let problem = $state<string | null>(null);
	let notice = $state<string | null>(null);
	let titleEl = $state<HTMLTextAreaElement | null>(null);
	let noticeTimer: ReturnType<typeof setTimeout> | undefined;

	// A confirmation is not a permanent label: it fades so the page is not left
	// saying "Saved." twenty minutes later.
	function say(message: string) {
		notice = message;
		clearTimeout(noticeTimer);
		noticeTimer = setTimeout(() => (notice = null), 4000);
	}

	// The title is a textarea so a long one wraps instead of clipping; it has to
	// be told its height on every change, including the first load.
	$effect(() => {
		void title;
		if (!titleEl) return;
		titleEl.style.height = 'auto';
		titleEl.style.height = `${titleEl.scrollHeight}px`;
	});

	const dirty = $derived(
		item !== null && (title !== item.title || note !== item.note || kind !== item.kind)
	);

	function message(error: unknown): string {
		if (isApiError(error)) return error.message;
		return error instanceof Error ? error.message : 'Something went wrong.';
	}

	function adopt(next: Item) {
		item = next;
		title = next.title;
		note = next.note;
		kind = next.kind;
	}

	async function load() {
		problem = null;
		try {
			const answer = await api.items.get(id);
			adopt(answer.item);
		} catch (error) {
			problem = message(error);
		} finally {
			loading = false;
		}
	}

	async function save() {
		if (!item || saving || !dirty) return;
		saving = true;
		problem = null;
		notice = null;
		try {
			const answer = await api.items.update(item.id, { title, note, kind });
			adopt(answer.item);
			say('Saved.');
		} catch (error) {
			problem = message(error);
		} finally {
			saving = false;
		}
	}

	async function remove() {
		if (!item || saving) return;
		saving = true;
		problem = null;
		try {
			await api.items.remove(item.id);
			await goto('/');
		} catch (error) {
			problem = message(error);
			saving = false;
		}
	}

	// Issue #76: a walled or dead save is filed, not failed, so this is the one
	// way to ask again — a wall that has lifted, or a link that came back. The
	// server clears the marker and the verdict, and the loop re-reads it.
	async function retry() {
		if (!item || saving) return;
		saving = true;
		problem = null;
		try {
			const answer = await api.items.retry(item.id);
			adopt(answer.item);
			say('Queued for another read.');
		} catch (error) {
			problem = message(error);
		} finally {
			saving = false;
		}
	}

	async function restore() {
		if (!item || saving) return;
		saving = true;
		problem = null;
		try {
			const answer = await api.items.restore(item.id);
			adopt(answer.item);
			confirmingDelete = false;
			say('Restored to the library.');
		} catch (error) {
			problem = message(error);
		} finally {
			saving = false;
		}
	}

	function hostOf(url: string | null): string {
		if (!url) return '';
		try {
			return new URL(url).hostname.replace(/^www\./, '');
		} catch {
			return url;
		}
	}

	function day(iso: string): string {
		return new Date(iso).toLocaleDateString(undefined, {
			year: 'numeric',
			month: 'short',
			day: 'numeric'
		});
	}

	function when(iso: string): string {
		return new Date(iso).toLocaleString(undefined, {
			year: 'numeric',
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
	}
	onMount(load);
</script>

<svelte:head>
	<title>{item ? item.title || 'Item' : 'Item'} — Harbor</title>
</svelte:head>

<div class="item-page">
	<p class="breadcrumb">
		<a href="/"><Icon name="arrow-left" size={15} /> Library</a>
	</p>

	{#if loading}
		<div class="empty"><p class="muted">Opening the item…</p></div>
	{:else if !item}
		<div class="empty">
			<h3>{problem ?? 'No such item.'}</h3>
			<p>It may have been purged, or the id is wrong.</p>
			<a class="btn" href="/">Back to the library</a>
		</div>
	{:else if item}
		{@const entry = item}
		{#if entry.deletedAt}
			<div class="callout amb banner" role="status">
				<Icon name="trash" size={16} />
				<span>This item is in Recently deleted. It is hidden from the library until restored.</span>
				<button class="btn sm" onclick={restore} disabled={saving}>Restore</button>
			</div>
		{/if}

		{#if item.cover}
			<!-- The same stored cover the card uses (#49). A failure hides the
			     element rather than leaving a broken-image box. -->
			<img
				class="item-cover"
				src={`/api/items/${item.id}/cover?v=${item.cover}`}
				alt=""
				width="1200"
				height="675"
				loading="lazy"
				decoding="async"
				onerror={(event) => ((event.currentTarget as HTMLImageElement).hidden = true)}
			/>
		{/if}

		<textarea
			class="in item-title"
			bind:value={title}
			bind:this={titleEl}
			rows={1}
			aria-label="Title"
			placeholder="Untitled"
		></textarea>

		<div class="metaline">
			<label class="kindpick">
				<Icon name={kindIcons[entry.kind]} size={14} />
				<select bind:value={kind} aria-label="Kind">
					{#each kinds as option (option.value)}
						<option value={option.value}>{option.label}</option>
					{/each}
				</select>
			</label>
			{#if entry.url}
				<a class="item-source" href={entry.url} target="_blank" rel="noreferrer">
					{hostOf(entry.url)}
					<Icon name="external-link" size={13} />
				</a>
				<i class="dot"></i>
			{/if}
			<span>Saved {day(entry.savedAt)}</span>
			{#if entry.readLater}
				<span class="pill amb"><Icon name="clock" size={13} /> Read later</span>
			{/if}
			{#if entry.state === 'failed'}
				<span class="pill amb"><Icon name="alert" size={13} /> Enrichment failed</span>
			{:else if entry.walled}
				<span class="pill amb"><Icon name="lock" size={13} /> Behind a wall</span>
			{:else if entry.linkStatus === 'dead'}
				<span class="pill amb"><Icon name="alert" size={13} /> Link dead</span>
			{/if}
		</div>

		{#if entry.state === 'failed'}
			<p class="callout amb" role="status">
				<Icon name="alert" size={15} />
				<span>{entry.lastError || 'Enrichment failed. Retry it from the library when you are ready.'}</span>
			</p>
		{:else if entry.walled || entry.linkStatus === 'dead'}
			<!-- Filed without its page, which is not a failure (issue #76): the row
			     keeps what the save carried, and this says what it does not have. -->
			<p class="callout amb" role="status">
				<Icon name="alert" size={15} />
				<span>
					{entry.walled
						? 'This site would not serve the page to Harbor, so there is no summary or tags. The title and your note are kept.'
						: 'The page is gone, so there is no summary or tags. The title, your note and the link are kept.'}
				</span>
			</p>
		{/if}

		{#if problem}
			<p class="callout amb" role="status">{problem}</p>
		{:else if notice}
			<p class="callout ok" role="status">{notice}</p>
		{/if}

		<div class="actions">
			{#if dirty}
				<button class="btn pri" onclick={save} disabled={saving}>
					{saving ? 'Saving…' : 'Save changes'}
				</button>
			{/if}
			<span class="sp"></span>

			{#if entry.walled || entry.linkStatus === 'dead'}
				<button class="btn ghost" onclick={retry} disabled={saving}>
					<Icon name="refresh" /> Try again
				</button>
			{/if}
			{#if confirmingDelete && !entry.deletedAt}
				<span class="dim">Move to Recently deleted?</span>
				<button class="btn ghost" onclick={() => (confirmingDelete = false)}>Cancel</button>
				<button class="btn danger" onclick={remove} disabled={saving}>Delete</button>
			{:else if !entry.deletedAt}
				<button class="btn ghost" onclick={() => (confirmingDelete = true)} disabled={saving}>
					<Icon name="trash" /> Delete
				</button>
			{/if}
		</div>

		<SummaryBlock summary={entry.summary} />

		<KindPanel kind={entry.kind} details={entry.details} />

		<label class="field">
			<span>Your note</span>
			<textarea class="ta" bind:value={note} placeholder="Anything worth remembering"></textarea>
		</label>

		<section class="sec">
			<div class="eyebrow">Tags</div>
			{#if entry.tags?.length}
				<TagRow tags={entry.tags} />
			{:else}
				<p class="hint">
					{entry.state === 'failed'
						? 'No tags: enrichment failed, so none were added.'
						: entry.walled || entry.linkStatus === 'dead'
							? 'No tags: the page could not be read, so none were added.'
							: 'No tags yet. Enrichment adds them, and they are edited once Spaces arrive.'}
				</p>
			{/if}
		</section>

		<section class="sec">
			<div class="eyebrow">Details</div>
			<dl class="prov">
				{#if entry.url}
					<dt>Address</dt>
					<dd>{entry.url}</dd>
				{/if}
				<dt>Saved</dt>
				<dd>{when(entry.savedAt)}, via {entry.source}</dd>
				{#if entry.createdAt !== entry.savedAt}
					<dt>First saved</dt>
					<dd>{when(entry.createdAt)}</dd>
				{/if}
				{#if entry.deletedAt}
					<dt>Status</dt>
					<dd>In Recently deleted</dd>
				{/if}
			</dl>
		</section>
	{/if}
</div>
