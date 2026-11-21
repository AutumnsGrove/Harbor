<script lang="ts">
	// The composer: a link, a thought, Read later. The merge warning is the
	// server's answer (/api/items/lookup), never a second implementation of
	// canonicalisation in the browser (plans/02 risks: merge drift).
	import { onMount } from 'svelte';

	import { api, isApiError, type Item } from '#lib/api';

	import Icon from './Icon.svelte';

	let {
		onclose,
		onsaved
	}: {
		onclose: () => void;
		onsaved: (item: Item, merged: boolean) => void;
	} = $props();

	let url = $state('');
	let note = $state('');
	let readLater = $state(false);
	let match = $state<Item | null>(null);
	let saving = $state(false);
	let problem = $state<string | null>(null);
	let urlField = $state<HTMLInputElement | null>(null);

	onMount(() => urlField?.focus());

	// Ask the server whether this link already exists, after typing settles. A
	// 422 while the URL is half-typed is not an error to show: the save is.
	$effect(() => {
		const candidate = url.trim();
		if (!candidate) {
			match = null;
			return;
		}
		let cancelled = false;
		const timer = setTimeout(async () => {
			try {
				const answer = await api.items.lookup(candidate);
				if (cancelled || url.trim() !== candidate) return;
				match = answer.exists && answer.item ? answer.item : null;
			} catch {
				if (!cancelled) match = null;
			}
		}, 350);
		return () => {
			cancelled = true;
			clearTimeout(timer);
		};
	});

	function message(error: unknown): string {
		if (isApiError(error)) return error.message;
		return error instanceof Error ? error.message : 'Could not save this.';
	}

	async function save(event: SubmitEvent) {
		event.preventDefault();
		if (saving) return;
		if (!url.trim() && !note.trim()) {
			problem = 'Add a link or a note.';
			return;
		}
		saving = true;
		problem = null;
		try {
			const result = await api.items.create({
				url: url.trim() || undefined,
				note: note.trim() || undefined,
				readLater,
				source: 'app'
			});
			onsaved(result.item, result.merged);
		} catch (error) {
			problem = message(error);
		} finally {
			saving = false;
		}
	}

	function dismiss(event: KeyboardEvent) {
		if (event.key === 'Escape' && !saving) onclose();
	}
</script>

<svelte:window onkeydown={dismiss} />

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
	class="scrim"
	role="presentation"
	onclick={() => {
		if (!saving) onclose();
	}}
>
	<div
		class="modal"
		role="dialog"
		aria-modal="true"
		tabindex="-1"
		aria-labelledby="composer-title"
		onclick={(event) => event.stopPropagation()}
	>
		<form onsubmit={save}>
		<h2 class="h2" id="composer-title">Save to Harbor</h2>
		<p class="note" style="margin-top: 4px">A link, a thought, or both. Enrichment arrives behind it.</p>

		<div class="stack" style="margin-top: 18px">
			<div class="field">
				<label for="composer-url">Link</label>
				<input
					class="in"
					id="composer-url"
					bind:this={urlField}
					bind:value={url}
					placeholder="https://…"
					autocomplete="off"
					spellcheck="false"
				/>
			</div>

			{#if match}
				<div class="callout" role="status">
					<span>
						Already saved as <b>{match.title || match.url}</b>. Saving again appends your note and
						refreshes the item.
					</span>
				</div>
			{/if}

			<div class="field">
				<label for="composer-note">Note</label>
				<textarea class="ta" id="composer-note" bind:value={note} placeholder="Why does this matter?"
				></textarea>
			</div>

			<label class="row switchrow">
				<input class="switch" type="checkbox" bind:checked={readLater} role="switch" />
				<span>Add to Read later</span>
			</label>
		</div>

		{#if problem}
			<p class="callout amb" role="status">
				<Icon name="alert" size={16} />
				<span>{problem}</span>
			</p>
		{/if}

		<div class="foot">
			<span class="hint">⌘↵ to save</span>
			<button class="btn ghost" type="button" onclick={onclose} disabled={saving}>Cancel</button>
			<button class="btn pri" type="submit" disabled={saving}>
				{saving ? 'Saving…' : match ? 'Merge into item' : 'Save'}
			</button>
		</div>
		</form>
	</div>
</div>
