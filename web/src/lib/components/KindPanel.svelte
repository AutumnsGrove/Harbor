<script lang="ts">
	// One panel per kind, built from the curated schema.org details the
	// extractor stored (server/internal/extract/jsonld.go). It renders only what
	// the page actually carried: no placeholder rows, and no section at all for
	// an item enrichment has not reached yet. That absence is the empty state.
	import type { ItemDetails, ItemKind } from '#lib/api';
	import { kindHeadings, kindIcons } from '#lib/kinds';
	import {
		addressLines,
		availabilityLabel,
		hasPanel,
		kindFacts,
		panelKind,
		priceLabel
	} from '#lib/details';

	import Icon from './Icon.svelte';

	let { kind, details }: { kind: ItemKind; details?: ItemDetails } = $props();

	// The library's kind wins; the panel follows JSON-LD only when the kind is
	// the article fallback and the page named something more specific.
	const shape = $derived(panelKind(kind, details?.type));
	const panel = $derived(hasPanel(kind, details));
	const rows = $derived(kindFacts(kind, details));

	const price = $derived(priceLabel(details?.offers));
	const available = $derived(availabilityLabel(details?.offers?.availability));
	const ingredients = $derived(details?.ingredients ?? []);
	const steps = $derived(details?.instructions ?? []);
	const address = $derived(addressLines(details?.address));
	const hours = $derived(details?.openingHours ?? []);
	const abstract = $derived((details?.abstract ?? '').trim());
	const offerUrl = $derived((details?.offers?.url ?? '').trim());
	const watchUrl = $derived((details?.embedUrl ?? details?.url ?? '').trim());

	// A price is only reassuring when the thing is actually buyable.
	const stocked = $derived(available.toLowerCase().startsWith('in stock'));
</script>

{#if panel}
	<section class="sec">
		<div class="eyebrow">
			<Icon name={kindIcons[shape]} size={13} />
			<span>{kindHeadings[shape]}</span>
		</div>

		{#if shape === 'product' && (price || available)}
			<div class="pricerow">
				{#if price}
					<p class="price">{price}</p>
				{/if}
				{#if available}
					<p class:okc={stocked} class:ambc={!stocked}>{available}</p>
				{/if}
			</div>
		{/if}

		{#if rows.length > 0}
			<dl class="facts">
				{#each rows as row (row.label)}
					<div>
						<dt>{row.label}</dt>
						<dd>{row.value}</dd>
					</div>
				{/each}
			</dl>
		{/if}

		{#if ingredients.length > 0 || steps.length > 0}
			<div class="cols2">
				{#if ingredients.length > 0}
					<div>
						<p class="lab">Ingredients</p>
						<ul class="ing">
							{#each ingredients as line, index (index)}
								<li>{line}</li>
							{/each}
						</ul>
					</div>
				{/if}
				{#if steps.length > 0}
					<div>
						<p class="lab">Method</p>
						<ol class="steps">
							{#each steps as step, index (index)}
								<li>{step}</li>
							{/each}
						</ol>
					</div>
				{/if}
			</div>
		{/if}

		{#if address.length > 0 || hours.length > 0}
			<div class="cols2">
				{#if address.length > 0}
					<div>
						<p class="lab">Address</p>
						<p class="addr">
							{#each address as line, index (index)}{line}{#if index < address.length - 1}<br />{/if}{/each}
						</p>
					</div>
				{/if}
				{#if hours.length > 0}
					<div>
						<p class="lab">Hours</p>
						<ul class="hours">
							{#each hours as line, index (index)}
								<li>{line}</li>
							{/each}
						</ul>
					</div>
				{/if}
			</div>
		{/if}

		{#if abstract}
			<div>
				<p class="lab">Abstract</p>
				<p class="abstract">{abstract}</p>
			</div>
		{/if}

		{#if offerUrl}
			<a class="item-source" href={offerUrl} target="_blank" rel="noreferrer">
				<Icon name="external-link" size={13} />
				<span>Open the offer</span>
			</a>
		{/if}

		{#if shape === 'video' && watchUrl}
			<a class="item-source" href={watchUrl} target="_blank" rel="noreferrer">
				<Icon name="play" size={13} />
				<span>Watch the original</span>
			</a>
		{/if}
	</section>
{/if}
