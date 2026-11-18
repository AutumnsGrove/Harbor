<script lang="ts">
	import { onMount } from 'svelte';

	import {
		api,
		isApiError,
		type AvailableResponse,
		type UpdateStatusResponse,
		type VersionResponse
	} from '#lib/api';

	type UpdateState = 'idle' | 'checking' | 'available' | 'requesting' | 'requested' | 'uptodate';

	let version = $state<VersionResponse | null>(null);
	let available = $state<AvailableResponse | null>(null);
	let lastUpdate = $state<UpdateStatusResponse | null>(null);
	let updateState = $state<UpdateState>('idle');
	let problem = $state<string | null>(null);

	function message(error: unknown): string {
		if (isApiError(error)) {
			if (error.status === 403) {
				return 'Not signed in through Tailscale. Open Harbor from its tailnet address.';
			}
			return error.message;
		}
		return error instanceof Error ? error.message : 'Something went wrong.';
	}

	async function load() {
		try {
			version = await api.version();
			lastUpdate = await api.update.status().catch(() => null);
			const answer = await api.update.available();
			available = answer;
			if (answer.available) updateState = 'available';
		} catch (error) {
			problem = message(error);
		}
	}

	async function checkForUpdates() {
		updateState = 'checking';
		problem = null;
		// "No update" and "the watcher never ran" both look like
		// `{"available":false}`, so completion has to come from status.json. A
		// finished run writes a new `at`; comparing it with the value seen before
		// asking is clock-independent — the phone and the potato never have to
		// agree on the time.
		const baseline = lastUpdate?.at ?? '';
		try {
			await api.update.check();
			const deadline = Date.now() + 15_000;
			while (Date.now() < deadline) {
				await new Promise((resolve) => setTimeout(resolve, 1_000));
				const answer = await api.update.available();
				available = answer;
				const status = await api.update.status().catch(() => null);
				if (status) lastUpdate = status;
				if (answer.available) {
					updateState = 'available';
					return;
				}
				if (status && status.status !== 'busy' && status.at && status.at !== baseline) {
					updateState = 'uptodate';
					return;
				}
			}
			updateState = 'idle';
			problem =
				'No answer from the watcher in 15 seconds. It may not be installed on this host yet (deploy/install.sh).';
		} catch (error) {
			updateState = 'idle';
			problem = message(error);
		}
	}

	// The page says it reloads when the new version answers, so it does. The
	// container is recreated underneath this page, so failed requests are the
	// expected state for a while, not an error to report.
	async function waitForNewVersion(before: string) {
		const deadline = Date.now() + 150_000;
		while (Date.now() < deadline) {
			await new Promise((resolve) => setTimeout(resolve, 2_000));
			try {
				const current = await api.version();
				if (current.version !== before) {
					location.reload();
					return;
				}
			} catch {
				// Being recreated: try again.
			}
		}
	}

	async function applyUpdate() {
		updateState = 'requesting';
		problem = null;
		try {
			await api.update.start();
			updateState = 'requested';
			void waitForNewVersion(version?.version ?? '');
		} catch (error) {
			updateState = 'available';
			problem = message(error);
		}
	}

	onMount(load);
</script>

<svelte:head>
	<title>Settings — Harbor</title>
</svelte:head>

<header class="pagehead">
	<h1 class="h1">Settings</h1>
	<p class="lead">
		The library settings arrive with their phases. What exists now is the deployment: the version,
		the update path, and what the watcher last did.
	</p>
</header>

<div class="stack">
	<section class="panel">
		<div class="row wrap">
			<div class="grow">
				<div class="h3">Version <span class="code">{version?.version ?? '…'}</span></div>
				<p class="note">
					{version ? version.deployment + ' deployment · image pinned by digest' : 'reading the running binary'}
				</p>
			</div>

			{#if updateState === 'idle'}
				<button class="btn" onclick={checkForUpdates}>Check for updates</button>
			{:else if updateState === 'checking'}
				<span class="pill">Checking…</span>
			{:else if updateState === 'available'}
				<span class="pill acc">update available</span>
				<button class="btn pri" onclick={applyUpdate}>Update now</button>
			{:else if updateState === 'requesting'}
				<span class="pill">Requesting…</span>
			{:else if updateState === 'requested'}
				<span class="pill ok">requested</span>
			{:else if updateState === 'uptodate'}
				<span class="pill ok">up to date</span>
			{/if}
		</div>

		{#if updateState === 'requested'}
			<div class="callout">
				<span>
					The watcher now pins the digest, snapshots the database with <span class="code">VACUUM INTO</span>,
					pulls, recreates the container and waits up to 120&nbsp;s for <span class="code">/healthz</span>.
					If the new version is not healthy it restores the snapshot and the previous digest. This page
					reloads when the new version answers.
				</span>
			</div>
		{/if}

		{#if available?.available}
			<div class="callout ok">
				<span>
					A newer image is available{available.version ? ` (${available.version})` : ''}. Applying it
					takes about a minute.
				</span>
			</div>
		{/if}

		{#if problem}
			<div class="callout amb" role="status">
				<span>{problem}</span>
			</div>
		{/if}
	</section>

	<section class="panel">
		<h2 class="h3">Last update</h2>
		{#if lastUpdate && lastUpdate.status !== 'unknown'}
			<p class="note">
				<span class="code">{lastUpdate.status}</span>{#if lastUpdate.version}
					· {lastUpdate.version}{/if}{#if lastUpdate.at} · {lastUpdate.at}{/if}
			</p>
		{:else}
			<p class="note">
				The watcher has not recorded a run yet. It writes <span class="code">status.json</span> after
				every update.
			</p>
		{/if}
	</section>

	<section class="panel">
		<h2 class="h3">Not built yet</h2>
		<p class="note">
			Tags, Spaces, import, Recently deleted, Sources and tokens, Status, Export and backup. Each one
			lands with its phase; none of them is faked here.
		</p>
	</section>
</div>
