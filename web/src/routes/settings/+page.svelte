<script lang="ts">
	import { onMount } from 'svelte';

	import {
		api,
		isApiError,
		type AvailableResponse,
		type Item,
		type UpdateStatusResponse,
		type UsageResponse,
		type VersionResponse
	} from '#lib/api';

	type UpdateState = 'idle' | 'checking' | 'available' | 'requesting' | 'requested' | 'uptodate';

	let version = $state<VersionResponse | null>(null);
	let available = $state<AvailableResponse | null>(null);
	let lastUpdate = $state<UpdateStatusResponse | null>(null);
	let updateState = $state<UpdateState>('idle');
	let problem = $state<string | null>(null);
	let deleted = $state<Item[]>([]);
	let deletedProblem = $state<string | null>(null);
	let restoring = $state<string | null>(null);
	let usage = $state<UsageResponse | null>(null);
	let usageProblem = $state<string | null>(null);

	// Settings → AI usage (issue #52). The budget comes from the server so the
	// bar has a denominator that is not hardcoded here.
	const monthPct = $derived(
		usage && usage.budgetUsd > 0 ? Math.min(100, (usage.monthToDateUsd / usage.budgetUsd) * 100) : 0
	);
	const overBudget = $derived(monthPct >= 70);
	const peakDay = $derived(usage ? Math.max(0, ...usage.byDay.map((day) => day.usd)) : 0);
	const backlogEstimate = $derived(usage ? usage.backlogItems * usage.avgCostPerItem : 0);

	function money(value: number): string {
		if (!Number.isFinite(value)) return '$0.00';
		// A call costs a fraction of a cent, so two decimals would read as zero.
		if (value > 0 && value < 0.01) return `$${value.toFixed(4)}`;
		return `$${value.toFixed(2)}`;
	}

	function calls(count: number): string {
		return `${count} call${count === 1 ? '' : 's'}`;
	}

	async function loadUsage() {
		try {
			usage = await api.usage.get(30);
			usageProblem = null;
		} catch (error) {
			usageProblem = message(error);
		}
	}

	function message(error: unknown): string {
		if (isApiError(error)) {
			if (error.status === 403) {
				return 'Not signed in through Tailscale. Open Harbor from its tailnet address.';
			}
			return error.message;
		}
		return error instanceof Error ? error.message : 'Something went wrong.';
	}

	async function loadDeleted() {
		try {
			const answer = await api.items.list({ deleted: true, limit: 50 });
			deleted = answer.items;
			deletedProblem = null;
		} catch (error) {
			deletedProblem = message(error);
		}
	}

	async function restore(entry: Item) {
		restoring = entry.id;
		try {
			await api.items.restore(entry.id);
			await loadDeleted();
		} catch (error) {
			deletedProblem = message(error);
		} finally {
			restoring = null;
		}
	}

	function deletedWhen(iso: string | undefined): string {
		if (!iso) return 'recently';
		return new Date(iso).toLocaleString(undefined, {
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
	}

	async function load() {
		void loadDeleted();
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

	onMount(() => {
		void load();
		void loadUsage();
	});
</script>

<svelte:head>
	<title>Settings — Harbor</title>
</svelte:head>

<header class="pagehead">
	<h1 class="h1">Settings</h1>
	<p class="lead">
		The library settings arrive with their phases. What exists now is the deployment: the version,
		the update path, the import, and what the watcher last did.
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
		<div class="row wrap">
			<div class="grow">
				<div class="h3">Import</div>
				<p class="note">
					Move your Raindrop library, or a browser's bookmarks, into Harbor. Merged by canonical link,
					so importing the same file twice is safe.
				</p>
			</div>
			<a class="btn" href="/settings/import">Open</a>
		</div>
	</section>

	<section class="panel">
		<div class="row wrap">
			<div class="grow">
				<div class="h3">AI usage</div>
				<p class="note">
					What the model calls cost. A save makes three: the kind, the title and summary, and the
					vector.
				</p>
			</div>
			{#if usage && usage.calls > 0}
				<span class="pill">{calls(usage.calls)} recorded</span>
			{/if}
		</div>

		{#if usageProblem}
			<p class="callout amb" role="status">{usageProblem}</p>
		{:else if !usage}
			<p class="note">Reading the usage table…</p>
		{:else if usage.calls === 0}
			<p class="note">
				No calls recorded yet. The first save that reaches a model appears here; an empty table is not
				the same claim as spending nothing.
			</p>
		{:else}
			<div class="usage-month">
				<div class="row">
					<span class="grow">{money(usage.monthToDateUsd)} this month</span>
					<span class="dim num" class:amb={overBudget}
						>{usage.budgetUsd > 0 ? `of ${money(usage.budgetUsd)}` : 'no budget set'}</span
					>
				</div>
				{#if usage.budgetUsd > 0}
					<svg
						class="usage-bar"
						viewBox="0 0 100 6"
						preserveAspectRatio="none"
						role="img"
						aria-label={`${monthPct.toFixed(0)} percent of the monthly budget`}
					>
						<rect class="track" width="100" height="6" />
						<rect class="fill" class:warn={overBudget} width={monthPct} height="6" />
					</svg>
				{/if}
				<p class="note">
					{money(usage.totalUsd)} all time over {usage.itemsWithUsage}
					{usage.itemsWithUsage === 1 ? 'item' : 'items'}
					{#if usage.itemsWithUsage > 0}· about {money(usage.avgCostPerItem)} a save{/if}
				</p>
			</div>

			{#if usage.backlogItems > 0}
				<p class="note">
					{usage.backlogItems} still queued ≈ {money(backlogEstimate)} at the recorded average. The
					provider bills as the queue drains, not before.
				</p>
			{/if}

			<svg
				class="spark"
				viewBox={`0 0 ${Math.max(usage.byDay.length * 4, 4)} 24`}
				preserveAspectRatio="none"
				role="img"
				aria-label="Spend per day for the last month"
			>
				{#each usage.byDay as day, index (day.day)}
					<rect
						x={index * 4 + 0.5}
						y={peakDay > 0 ? 23 - Math.max(1, (day.usd / peakDay) * 21) : 22}
						width="3"
						height={peakDay > 0 ? Math.max(1, (day.usd / peakDay) * 21) : 1}
					/>
				{/each}
			</svg>

			<div class="rows">
				{#each usage.bySurface as group (group.key)}
					<div>
						<div class="grow">
							<div class="t">{group.key}</div>
							<div class="s">{calls(group.calls)}</div>
						</div>
						<span class="num dim">{money(group.usd)}</span>
					</div>
				{/each}
			</div>
		{/if}

		<p class="note">
			This is the OpenRouter side only. Cloudflare's D1, R2 and browser rendering have their own bill,
			which the potato cannot see.
		</p>
	</section>

	<section class="panel">
		<h2 class="h3">Recently deleted</h2>
		{#if deletedProblem}
			<p class="callout amb" role="status">{deletedProblem}</p>
		{:else if deleted.length === 0}
			<p class="note">Nothing in the bin. Deleted items stay here for 30 days, then they are purged.</p>
		{:else}
			<div class="rows">
				{#each deleted as entry (entry.id)}
					<div>
						<div class="grow">
							<div class="t">{entry.title || entry.url || 'Untitled note'}</div>
							<div class="s">deleted {deletedWhen(entry.deletedAt)} · {entry.kind}</div>
						</div>
						<button class="btn sm" onclick={() => restore(entry)} disabled={restoring === entry.id}>
							{restoring === entry.id ? 'Restoring…' : 'Restore'}
						</button>
					</div>
				{/each}
			</div>
		{/if}
	</section>

	<section class="panel">
		<h2 class="h3">Not built yet</h2>
		<p class="note">
			Tags, Spaces, Sources and tokens, Search, Status, Export and backup. Each one lands with its
			phase; none of them is faked here.
		</p>
	</section>
</div>
