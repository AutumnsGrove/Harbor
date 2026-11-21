<script lang="ts">
	// The model's summary of the page, labelled so it is never mistaken for the
	// owner's own note. An empty summary hides the block entirely.
	let { summary }: { summary: string } = $props();
</script>

{#if summary.trim()}
	<section class="sumbox">
		<div class="eyebrow">Summary <span class="dim">· written by AI</span></div>
		<p>{summary}</p>
	</section>
{/if}
