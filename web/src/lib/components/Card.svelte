<script lang="ts">
	// One card in the masonry feed. The cover is the page's own og:image, proxied
	// by this origin (issue #49); a card without one keeps the gradient and the
	// kind glyph, which is the honest placeholder for a note or a failed fetch.
	import type { Item } from '#lib/api';

	import { kindIcons } from '#lib/kinds';

	import Icon from './Icon.svelte';

	let { item }: { item: Item } = $props();

	// A cover-less card is a placeholder, and a wall of identical placeholders
	// reads as broken. The item id seeds where the light falls on each one, so
	// the same item always looks the same and its neighbours differ.
	// FNV-1a plus a murmur-style finalizer: ids saved seconds apart share most of
	// their characters, and a plain multiply-and-add leaves neighbours with the
	// same proportion, which is the rote look this is here to break.
	const seed = $derived.by(() => {
		let h = 2166136261;
		for (const ch of item.id) h = Math.imul(h ^ ch.charCodeAt(0), 16777619);
		h = Math.imul(h ^ (h >>> 16), 2246822507);
		h = Math.imul(h ^ (h >>> 13), 3266489909);
		return (h ^ (h >>> 16)) >>> 0;
	});

	// Masonry only reads as masonry if the tiles differ in height. We do not know
	// an og:image's real shape before it loads (and a late load must not reflow
	// the column, #49), so the cover's proportion is chosen up front: stable per
	// item, weighted by kind (video is wide, a book or product is tall), and the
	// image is cropped to fit with object-fit.
	const ratios: Record<string, string[]> = {
		video: ['16 / 9', '16 / 9', '16 / 10'],
		movie: ['2 / 3', '3 / 4', '16 / 9'],
		podcast: ['1 / 1', '1 / 1', '4 / 3'],
		book: ['3 / 4', '2 / 3', '3 / 4'],
		product: ['4 / 5', '1 / 1', '3 / 4'],
		paper: ['3 / 4', '4 / 5', '1 / 1'],
		image: ['1 / 1', '4 / 5', '3 / 4', '4 / 3', '16 / 10'],
		place: ['1 / 1', '4 / 3', '5 / 4'],
		recipe: ['4 / 5', '1 / 1', '4 / 3']
	};
	const fallbackRatios = ['16 / 10', '4 / 3', '1 / 1', '4 / 5', '16 / 9', '5 / 4', '3 / 4'];
	const ratio = $derived.by(() => {
		const choices = ratios[item.kind] ?? fallbackRatios;
		return choices[(seed >>> 8) % choices.length];
	});

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

	// A save the page would not give up is filed, not failed (issue #76): the
	// card says why it is thin instead of looking like a broken fetch. Walled is
	// the site refusing this client; Link dead is the page being gone.
	const badge = $derived(item.walled ? 'Walled' : item.linkStatus === 'dead' ? 'Link dead' : '');
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
		<div class="cover" style:--ax={`${seed % 70}%`} style:--ay={`${(seed >>> 3) % 60}%`} style:--ratio={ratio}>
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
