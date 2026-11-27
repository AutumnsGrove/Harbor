<script lang="ts">
	// The wordmark: "harbor" with the o drawn as a ring around a lit core. Set
	// in the page's own face so there is no image to load; the ring is CSS so it
	// scales with the font size. `lit` turns the core on, which is what the
	// empty and loading states do while Harbor is waiting for something.
	let { size = 22, lit = true }: { size?: number; lit?: boolean } = $props();
</script>

<span class="wordmark" class:unlit={!lit} style:font-size={`${size}px`} role="img" aria-label="Harbor">
	<span aria-hidden="true">harb<span class="ring"></span>r</span>
</span>
