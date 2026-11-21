<script lang="ts">
	// The item page skeleton: title, note, kind, and the delete/restore pair.
	// Media, reader and highlights arrive with phases 6 and 8; the placeholders
	// say so rather than pretending.
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { onMount } from 'svelte';

	import { api, isApiError, type Item, type ItemKind } from '#lib/api';

	import Icon from '../../../lib/components/Icon.svelte';

	const kinds: { value: ItemKind; label: string }[] = [
		{ value: 'article', label: 'Article' },
		{ value: 'product', label: 'Product' },
		{ value: 'book', label: 'Book' },
		{ value: 'recipe', label: 'Recipe' },
		{ value: 'video', label: 'Video' },
		{ value: 'tweet', label: 'Post' },
		{ value: 'note', label: 'Note' },
		{ value: 'image', label: 'Image' },
		{ value: 'paper', label: 'Paper' },
		{ value: 'podcast', label: 'Podcast' },
		{ value: 'place', label: 'Place' },
		{ value: 'movie', label: 'Movie' }
	];

	const id = $derived(page.params.id ?? '');

	let item = $state<Item | null>(null);
	let title = $state('');
	let note = $state('');
	let kind = $state<ItemKind>('article');
	let loading = $state(true);
	let saving = $state(false);
	let confirmingDelete = $state(false);
	let problem = $state<string | null>(null);
	let notice = $state<string | null>(null);

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
			notice = 'Saved.';
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

	async function restore() {
		if (!item || saving) return;
		saving = true;
		problem = null;
		try {
			const answer = await api.items.restore(item.id);
			adopt(answer.item);
			confirmingDelete = false;
			notice = 'Restored to the library.';
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

	<div class="item-grid">
		<aside class="item-aside">
			<div class="item-cover">
				<Icon name={entry.kind === 'note' ? 'file-text' : 'link'} size={30} />
				<span>{kinds.find((k) => k.value === entry.kind)?.label ?? 'Item'}</span>
			</div>
			{#if entry.url}
				<a class="item-source" href={entry.url} target="_blank" rel="noreferrer">
					<span>{hostOf(entry.url)}</span>
					<Icon name="external-link" size={14} />
				</a>
			{/if}
			<dl class="meta">
				<div><dt>Source</dt><dd>{entry.source}</dd></div>
				<div><dt>Saved</dt><dd>{when(entry.savedAt)}</dd></div>
				{#if entry.createdAt !== entry.savedAt}
					<div><dt>First saved</dt><dd>{when(entry.createdAt)}</dd></div>
				{/if}
				{#if entry.readLater}
					<div><dt>Space</dt><dd class="ambc">Read later</dd></div>
				{/if}
			</dl>
			{#if entry.state === 'failed'}
				<p class="pill amb"><Icon name="alert" size={13} /> Enrichment failed</p>
			{/if}
		</aside>

		<div class="item-body">
			<input class="in item-title" bind:value={title} aria-label="Title" placeholder="Untitled" />

			<label class="field">
				<span>Note</span>
				<textarea class="ta" bind:value={note} placeholder="Anything worth remembering"></textarea>
			</label>

			<div class="field">
				<label for="item-kind">Kind</label>
				<select class="in" id="item-kind" bind:value={kind}>
					{#each kinds as option (option.value)}
						<option value={option.value}>{option.label}</option>
					{/each}
				</select>
			</div>

			{#if problem}
				<p class="callout amb" role="status">{problem}</p>
			{:else if notice}
				<p class="callout ok" role="status">{notice}</p>
			{/if}

			<div class="actions">
				<button class="btn pri" onclick={save} disabled={saving || !dirty}>
					{saving ? 'Saving…' : dirty ? 'Save changes' : 'Saved'}
				</button>
				<span class="sp"></span>

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
		</div>
	</div>
{/if}
