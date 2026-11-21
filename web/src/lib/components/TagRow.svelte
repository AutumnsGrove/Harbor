<script lang="ts">
	// The item page's tag row. Read-only until phase 9's tag editor: the add box
	// and the AI/manual distinction land with the tag manager, and pretending to
	// offer them now would be a control that does nothing.
	import Icon from './Icon.svelte';

	let { tags }: { tags: string[] } = $props();
</script>

{#if tags.length > 0}
	<div class="tagrow">
		{#each tags as tag (tag)}
			<span class="tag"><Icon name="tag" size={11} />{tag}</span>
		{/each}
	</div>
{/if}
