<script lang="ts">
	// Settings → Import (plans/05). The CLI is still the real interface for a big
	// file; this is the same run from the door a human would use, which is what
	// the phase's exit demo requires.
	//
	// The flow is the one the plan asks for: drop the file, read the dry run,
	// then commit. Progress is polled — the run's live numbers are bytes read,
	// its durable numbers are the checkpoint in import_runs.
	import { onMount } from 'svelte';

	import {
		api,
		isApiError,
		type ImportDryRunResponse,
		type ImportReport,
		type ImportStatusResponse
	} from '#lib/api';

	type Phase = 'idle' | 'reading' | 'preview' | 'running' | 'finished';

	let phase = $state<Phase>('idle');
	let file = $state<File | null>(null);
	let preview = $state<ImportDryRunResponse | null>(null);
	let status = $state<ImportStatusResponse | null>(null);
	let enrich = $state(false);
	let problem = $state<string | null>(null);
	let cancelled = $state(false);
	let dragging = $state(false);
	let pollTimer: ReturnType<typeof setTimeout> | undefined;

	const number = new Intl.NumberFormat();

	function message(error: unknown): string {
		if (isApiError(error)) {
			if (error.status === 403) {
				return 'Not signed in through Tailscale. Open Harbor from its tailnet address.';
			}
			return error.message;
		}
		return error instanceof Error ? error.message : 'Something went wrong.';
	}

	function choose(event: Event) {
		const input = event.currentTarget as HTMLInputElement;
		const picked = input.files?.[0];
		input.value = '';
		if (picked) void readPreview(picked);
	}

	function drop(event: DragEvent) {
		event.preventDefault();
		dragging = false;
		const picked = event.dataTransfer?.files?.[0];
		if (picked) void readPreview(picked);
	}

	// The dry run is the safety net the plan asks for: it reads the whole file,
	// reports what would happen and writes nothing, so the numbers on screen are
	// what the import will do rather than what it hoped to do.
	async function readPreview(picked: File) {
		file = picked;
		preview = null;
		problem = null;
		phase = 'reading';
		try {
			preview = await api.import.dryRun(picked);
			phase = 'preview';
		} catch (error) {
			phase = 'idle';
			problem = message(error);
		}
	}

	async function start() {
		if (!file) return;
		problem = null;
		cancelled = false;
		try {
			status = await api.import.start(file, { enrich });
			phase = 'running';
			void poll();
		} catch (error) {
			problem = message(error);
		}
	}

	// Polling, not streaming: one request a second is enough for a bar, and the
	// page picks a run back up after a reload because the status is durable.
	async function poll() {
		clearTimeout(pollTimer);
		try {
			status = await api.import.status();
			problem = null;
		} catch (error) {
			problem = message(error);
		}
		if (status?.running) {
			pollTimer = setTimeout(() => void poll(), 1000);
			return;
		}
		phase = 'finished';
	}

	async function cancel() {
		cancelled = true;
		try {
			await api.import.cancel();
		} catch (error) {
			// 409: it finished before the cancel landed. The poll says what happened.
			problem = isApiError(error) && error.status === 409 ? null : message(error);
		}
	}

	function startOver() {
		clearTimeout(pollTimer);
		phase = 'idle';
		file = null;
		preview = null;
		status = null;
		problem = null;
		cancelled = false;
	}

	// The report is a file the operator keeps, so it is a download rather than a
	// screen they have to screenshot.
	function download() {
		const report = status?.run?.report;
		if (!report) return;
		const blob = new Blob([JSON.stringify(report, null, 2)], { type: 'application/json' });
		const link = document.createElement('a');
		link.href = URL.createObjectURL(blob);
		link.download = 'harbor-import-report.json';
		link.click();
		URL.revokeObjectURL(link.href);
	}

	const read = $derived(status?.bytesRead ?? 0);
	const total = $derived(status?.bytesTotal ?? 0);
	const percent = $derived(total > 0 ? Math.min(100, Math.round((read / total) * 100)) : 0);
	const records = $derived(status?.run?.checkpoint ?? 0);

	function report(): ImportReport | null {
		return preview?.report ?? status?.run?.report ?? null;
	}

	function finishedWhen(iso: string | null | undefined): string {
		if (!iso) return '';
		return new Date(iso).toLocaleString(undefined, {
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
	}

	onMount(() => {
		void (async () => {
			try {
				status = await api.import.status();
			} catch {
				return;
			}
			// An import this tab did not start — or one still going after a reload.
			if (status.running) {
				phase = 'running';
				void poll();
			}
		})();
		return () => clearTimeout(pollTimer);
	});
</script>

<svelte:head>
	<title>Import — Harbor</title>
</svelte:head>

<header class="pagehead">
	<h1 class="h1">Import</h1>
	<p class="lead">
		Move a Raindrop CSV export or a browser's <span class="code">bookmarks.html</span> in. Everything is
		merged by canonical link, so importing the same file twice adds nothing the second time and a
		cancelled run is safe to start again.
	</p>
</header>

<div class="stack">
	{#if status?.resumable && phase === 'idle'}
		<div class="callout amb" role="status">
			<span>
				An earlier import stopped at record {number.format(status.run?.checkpoint ?? 0)} and never
				finished. Choosing the same file again continues from there instead of starting over.
			</span>
		</div>
	{/if}

	<section class="panel">
		<h2 class="h3">The file</h2>
		<p class="note">
			The file is read here and written into the library on the potato; only enrichment ever sends
			anything further. Collections become Spaces, and highlights survive — they are the one thing in
			an export that is tedious to recreate.
		</p>

		{#if phase === 'idle' || phase === 'reading'}
			<label
				class="drop"
				class:dragging
				for="import-file"
				ondragover={(event) => {
					event.preventDefault();
					dragging = true;
				}}
				ondragleave={() => (dragging = false)}
				ondrop={drop}
			>
				{#if phase === 'reading'}
					<span>Reading the file…</span>
				{:else}
					<span>Drop the export here, or choose a file</span>
				{/if}
				<em class="dim">Raindrop CSV, or any browser's bookmarks.html</em>
				<input
					id="import-file"
					type="file"
					accept=".csv,.html,.htm,text/csv,text/html"
					onchange={choose}
					disabled={phase === 'reading'}
				/>
			</label>
		{:else}
			<div class="rows">
				<div>
					<div class="grow">
						<div class="t">{file?.name ?? 'the import file'}</div>
						<div class="s">
							{#if file}{Math.max(1, Math.round(file.size / 1024))} kB · {/if}{preview?.report
								.total
								? `${number.format(preview.report.total)} rows`
								: `${number.format(records)} records read`}
						</div>
					</div>
					{#if phase === 'preview'}
						<button class="btn" onclick={startOver}>Choose another</button>
					{:else if phase === 'finished'}
						<button class="btn" onclick={startOver}>Done</button>
					{/if}
				</div>
			</div>
		{/if}

		{#if problem}
			<div class="callout amb" role="status">
				<span>{problem}</span>
			</div>
		{/if}
	</section>

	{#if phase === 'preview' && preview}
		<section class="panel">
			<div class="row wrap">
				<div class="grow">
					<h2 class="h3">What the import would do</h2>
					<p class="note">
						Nothing has been written yet. The counts below come from reading your file against the
						library as it is now.
					</p>
				</div>
			</div>

			<div class="report">
				<div><strong>{number.format(preview.report.imported)}</strong><span>new saves</span></div>
				<div><strong>{number.format(preview.report.merged)}</strong><span>merge into what is here</span></div>

				<div>
					<strong>{number.format(preview.report.spaces_to_create ?? 0)}</strong><span>Spaces to create</span>
				</div>
				<div><strong>{number.format(preview.report.highlights)}</strong><span>highlights</span></div>
				<div><strong>{number.format(preview.report.favourites)}</strong><span>favourites</span></div>
				{#if preview.report.skipped > 0}
					<div><strong>{number.format(preview.report.skipped)}</strong><span>skipped</span></div>
				{/if}
				{#if preview.report.failed > 0}
					<div><strong>{number.format(preview.report.failed)}</strong><span>failed</span></div>
				{/if}
			</div>

			{#if status?.resumable}
				<div class="callout amb">
					<span>
						An earlier import stopped at record {number.format(status.run?.checkpoint ?? 0)}. If
						this is the same file, the run continues from there — the counts above describe the
						whole file, not what is left to read.
					</span>
				</div>
			{/if}

			{#if preview.report.unknown_columns?.length}
				<div class="callout amb">
					<span>
						Columns this build does not know, ignored: <span class="code"
							>{preview.report.unknown_columns.join(', ')}</span
						>. Raindrop changed its export; the import is safe, but those fields are not carried.
					</span>
				</div>
			{/if}

			{#each preview.report.file_warnings ?? [] as warning (warning)}
				<p class="note">{warning}</p>
			{/each}

			<label class="check">
				<input type="checkbox" bind:checked={enrich} />
				<span>
					Queue these for the model afterwards. It reads every new save, tags it and summarises it —
					about $0.0015 a save, so roughly
					<span class="code">${((preview.report.imported * 0.0015) || 0).toFixed(2)}</span> for this
					file, and it drains behind anything you save by hand. Left off, the saves land ready to
					use and can be enriched later.
				</span>
			</label>

			<div class="row wrap actions">
				<button class="btn pri" onclick={start}>
					Import {number.format(preview.report.total)} rows
				</button>
				<span class="note">A snapshot first is not a bad idea: <span class="code">harbor backup</span></span>
			</div>
		</section>
	{/if}

	{#if phase === 'running' || phase === 'finished'}
		<section class="panel">
			<div class="row wrap">
				<div class="grow">
					<h2 class="h3">
						{#if phase === 'running'}
							Importing{status?.run?.checkpoint
								? ' · ' + number.format(records) + ' records read'
								: '…'}
						{:else if status?.run?.state === 'cancelled'}
							Import stopped
						{:else}
							Import finished
						{/if}
					</h2>
					<p class="note">
						{#if phase === 'running'}
							The run continues even if you close this tab, and nothing else can start an import
							while it is going.
						{:else if status?.run?.finishedAt}
							{status.run.state === 'cancelled' ? 'Stopped' : 'Finished'}
							{finishedWhen(status.run.finishedAt)}. Importing the same file again merges rather than
							duplicates.
						{/if}
					</p>
				</div>
				{#if phase === 'running'}
					<button class="btn" onclick={cancel} disabled={cancelled}>
						{cancelled ? 'Stopping…' : 'Stop'}
					</button>
				{:else if status?.run?.report}
					<button class="btn" onclick={download}>Download report</button>
				{/if}
			</div>

			{#if phase === 'running'}
				<progress class="bar" value={read} max={total || 1}
					>Importing: {percent}%</progress
				>
				<p class="note">
					{number.format(read)} of {number.format(total)} bytes · {percent}%
				</p>
			{/if}

			{#if report()}
				<div class="report">
					<div><strong>{number.format(report()?.imported ?? 0)}</strong><span>new saves</span></div>
					<div><strong>{number.format(report()?.merged ?? 0)}</strong><span>merged</span></div>
					<div><strong>{number.format(report()?.spaces_created ?? 0)}</strong><span>Spaces</span></div>
					<div><strong>{number.format(report()?.highlights ?? 0)}</strong><span>highlights</span></div>
					<div><strong>{number.format(report()?.favourites ?? 0)}</strong><span>favourites</span></div>
					{#if (report()?.skipped ?? 0) > 0}
						<div><strong>{number.format(report()?.skipped ?? 0)}</strong><span>skipped</span></div>
					{/if}
					{#if (report()?.failed ?? 0) > 0}
						<div><strong>{number.format(report()?.failed ?? 0)}</strong><span>failed</span></div>
					{/if}
				</div>
			{/if}

			{#each report()?.errors ?? [] as row (row.record)}
				<p class="note">
					record {number.format(row.record)}
					{#if row.url}<span class="code">{row.url}</span>{/if} — {row.reason}
				</p>
			{/each}

			{#if phase === 'finished' && status?.run?.state === 'cancelled'}
				<div class="callout amb">
					<span>
						The run stopped at record {number.format(status.run.checkpoint)}. Choose the same file
						again to continue from there; the rows it already read are not re-read.
					</span>
				</div>
			{/if}

			{#if phase === 'finished' && status?.run}
				<p class="note">Open the library and look at what landed: <a href="/">the grid</a>.</p>
			{/if}
		</section>
	{/if}

	<section class="panel">
		<h2 class="h3">The CLI still does this</h2>
		<p class="note">
			A 1,500-row export is a minute of writing either way, but
			<span class="code">harbor import FILE --dry-run</span> is the version that survives a closed
			laptop. <span class="code">docs/import.md</span> is the operator's page for it.
		</p>
	</section>
</div>
