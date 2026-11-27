<script lang="ts">
	// One card in the masonry feed. The cover is the page's own og:image, proxied
	// by this origin (issue #49); a card without one keeps the gradient and the
	// kind glyph, which is the honest placeholder for a note or a failed fetch.
	import type { Item } from '#lib/api';
	import { coverRatio, lightAt, seedOf } from '#lib/art';
	import { kindIcons } from '#lib/kinds';

	import Icon from './Icon.svelte';

	let { item }: { item: Item } = $props();

	// A wall of identical placeholders reads as broken, so the item id decides
	// the proportion and where the light falls (lib/art.ts). The item page uses
	// the same seed, so the card and the page it opens look like one thing.
	const seed = $derived(seedOf(item.id));
	const ratio = $derived(coverRatio(item.kind, seed));
	const light = $derived(lightAt(seed));

	// A cover that fails to load is the same as no cover: one silent fallback,
	// not a broken-image icon.
	let coverFailed = $state(false);
	const showCover = $derived(Boolean(item.cover) && !coverFailed);

	const kindNames: Record<string, string> = {
		article: 'Article',
		product: 'Product',
		book: 'Book',
		recipe: 'Recipe',
		video: 'Video',
		tweet: 'Post',
		note: 'Note',
		image: 'Image',
		paper: 'Paper',
		podcast: 'Podcast',
		place: 'Place',
		movie: 'Movie'
	};

	const isText = $derived(item.kind === 'note' || item.kind === 'tweet');

	// A save the page would not give up is filed, not failed (issues #74, #76):
	// the card says why it is thin instead of looking like a broken fetch.
	// Walled is the site refusing this client, Link dead is the page being gone,
	// No text is a document that was read and had nothing in it, and Too large is
	// a body past its cap — which no retry makes smaller.
	const badge = $derived(
		item.walled
			? 'Walled'
			: item.linkStatus === 'dead'
				? 'Link dead'
				: item.noText
					? 'No text'
					: item.tooLarge
						? 'Too large'
						: ''
	);
	const host = $derived(hostOf(item.url));
	const age = $derived(relative(item.savedAt));
	const excerpt = $derived(firstLine(item.note || item.summary));

	function hostOf(url: string | null): string {
		if (!url) return 'In-app';
		try {
			return new URL(url).hostname.replace(/^www\./, '');
		} catch {
			return url;
		}
	}

	function firstLine(text: string): string {
		const line = text.trim().split('\n')[0] ?? '';
		return line.length > 160 ? `${line.slice(0, 157)}…` : line;
	}

	function relative(iso: string): string {
		const then = new Date(iso).getTime();
		if (Number.isNaN(then)) return '';
		const seconds = Math.max(0, Math.round((Date.now() - then) / 1000));
		if (seconds < 60) return 'now';
		const minutes = Math.round(seconds / 60);
		if (minutes < 60) return `${minutes}m`;
		const hours = Math.round(minutes / 60);
		if (hours < 24) return `${hours}h`;
		const days = Math.round(hours / 24);
		if (days < 30) return `${days}d`;
		return new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
	}
</script>

<a class="card" class:text={isText} href={`/item/${item.id}`} aria-label={item.title}>
	{#if isText}
		<div class="body">
			<p class="tx">{excerpt || 'Empty note'}</p>
			<p class="sub">
				<Icon name="file-text" size={13} />
				<span>{kindNames[item.kind] ?? 'Note'} · {age}</span>
				{#if badge}
					<span class="flag">{badge}</span>
				{/if}
			</p>
		</div>
	{:else}
		<div class="cover" style:--ax={light.x} style:--ay={light.y} style:--ratio={ratio}>
			{#if showCover}
				<img
					class="coverimg"
					src={`/api/items/${item.id}/cover?v=${item.cover}`}
					alt=""
					width="640"
					height="400"
					loading="lazy"
					decoding="async"
					onerror={() => (coverFailed = true)}
				/>
			{:else}
				<span class="kindmark">
					<Icon name={kindIcons[item.kind] ?? 'link'} size={24} />
				</span>
				<span class="kindtag">{kindNames[item.kind] ?? 'Link'}</span>
			{/if}
			{#if item.readLater}
				<span class="rl" title="Read later"><Icon name="clock" size={14} /></span>
			{/if}
		</div>
		<div class="body">
			<h3>{item.title || host}</h3>
			<p class="sub">
				<span>{host}</span><span>· {age}</span>
				{#if badge}
					<span class="flag">{badge}</span>
				{/if}
			</p>
			{#if excerpt}
				<p class="more">{excerpt}</p>
			{/if}
		</div>
	{/if}
</a>
