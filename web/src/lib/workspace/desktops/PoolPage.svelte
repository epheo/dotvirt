<script lang="ts">
	import type { DesktopPoolCounts } from '#lib/api.js';
	import {
		assignment,
		count,
		joinClaims,
		listed,
		poolState,
		projectOf,
		readyOf,
		unlistedLines,
	} from '#lib/desktops.js';
	import { plural } from '#lib/format.js';
	import type { Scope } from '#lib/lenses.js';
	import { hrefForScope, trailForScope } from '#lib/nav.js';
	import { desktops } from '#lib/state/desktops.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import Card from '#lib/components/Card.svelte';
	import ClassMissing from './ClassMissing.svelte';
	import DesktopTable from './DesktopTable.svelte';
	import DesktopsFrame from './DesktopsFrame.svelte';
	import RepoLink from './RepoLink.svelte';
	import UnlistedNote from './UnlistedNote.svelte';

	// One pool: how its seats stand, whether DeskuBe can make its desktops,
	// and its desktops with the people holding them.
	let { scope }: { scope: Extract<Scope, { kind: 'pool' }> } = $props();

	const data = $derived(desktops.value?.synced ? desktops.value : null);
	const pool = $derived(
		data?.pools.find((p) => p.namespace === scope.namespace && p.name === scope.pool),
	);
	const st = $derived(pool && poolState(pool));
	const ready = $derived(pool && readyOf(pool.conditions));
	const owners = $derived(projectOf(inventory.projects));
	const nsHref = $derived(
		hrefForScope(
			{ kind: 'namespace', project: scope.project, namespace: scope.namespace },
			'desktops',
		),
	);
	const here = $derived(new Set([scope.namespace]));

	const STATS: { key: keyof DesktopPoolCounts; label: string; title: string }[] = [
		{ key: 'held', label: 'Held', title: 'Desktops a claim holds, stopped ones included' },
		{ key: 'warm', label: 'Warm', title: 'Running desktops no claim holds yet' },
		{ key: 'free', label: 'Free', title: 'Seats without a desktop' },
		{ key: 'waiting', label: 'Waiting', title: 'Claims with no seat yet' },
		{ key: 'inSession', label: 'In session', title: 'Desktops a client streams from' },
		{ key: 'ready', label: 'Ready', title: 'Desktops a client can reach, warm ones included' },
		{ key: 'stopped', label: 'Stopped', title: 'Held desktops their claim stopped' },
	];
	const ROW =
		'grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-3 border-t border-line-soft py-2 first:border-t-0';
</script>

<DesktopsFrame
	trail={trailForScope(scope, 'Desktops', (p) => inventory.title(p), 'desktops')}
	title={scope.pool}
	status={st && { tone: st.tone, text: st.label }}
	context={pool ? `${assignment(pool)} pool of class ${pool.class} in ${pool.namespace}` : ''}
>
	{#snippet children(d)}
		{#if !pool}
			{#if !listed(d, scope.namespace, 'vdipools')}
				<UnlistedNote lines={unlistedLines(d.unlisted, here, true)} />
			{:else}
				<p class="text-[13px] text-ink-muted">
					No pool {scope.pool} in {scope.namespace}: it was deleted, or is not applied yet.
					<a class="text-accent-ink hover:underline" href={nsHref}>See {scope.namespace}</a>
				</p>
			{/if}
		{:else}
			{#if st?.missingClass}
				<ClassMissing
					name={st.missingClass}
					outcome="this pool makes no desktop and its claims wait"
					project={scope.project}
					classes={d.classes}
				/>
			{:else if st?.message}
				<p
					class="rounded-lg border border-warn/50 bg-warn-soft px-4 py-3 text-[13px] text-warn-ink"
				>
					DeskuBe reports this pool not ready: {st.message}
				</p>
			{:else if pool.closed}
				<p class="rounded-lg border border-line bg-inset px-4 py-3 text-[13px] text-ink-soft">
					Closed: claims without a desktop wait, and none is kept warm. Reopen it in
					<RepoLink project={scope.project} />.
				</p>
			{/if}

			<div class="grid grid-cols-[repeat(auto-fit,minmax(min(360px,100%),1fr))] items-start gap-4">
				<Card title="Seats" meta="{count(pool, 'held')} of {pool.seats} held">
					<dl class="grid grid-cols-4 gap-x-4 gap-y-3">
						<div>
							<dt class="text-xs text-ink-muted">Seats</dt>
							<dd class="text-xl font-bold text-ink tabular-nums">{pool.seats}</dd>
						</div>
						{#each STATS as s (s.key)}
							<div title={s.title}>
								<dt class="text-xs text-ink-muted">{s.label}</dt>
								<dd class="text-xl font-bold text-ink tabular-nums">{count(pool, s.key)}</dd>
							</div>
						{/each}
					</dl>
					{#if count(pool, 'waiting') && !st?.missingClass}
						<p class="mt-3 text-[13px] text-ink-muted">
							{plural(count(pool, 'waiting'), 'claim')} waiting for a seat: raise the seats in
							<RepoLink project={scope.project} /> to seat them.
						</p>
					{/if}
				</Card>

				<Card title="Pool">
					<dl class="text-[13px]">
						<div class={ROW}>
							<dt class="text-ink-muted">Class</dt>
							<dd class="font-mono text-xs">{pool.class}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Assignment</dt>
							<dd>
								{assignment(pool) === 'Shared'
									? 'Shared: any free desktop, given back when its person leaves'
									: 'Personal: each person keeps a desktop and its home'}
							</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Keeps warm</dt>
							<dd>{plural(pool.keepWarm, 'desktop')}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Ready</dt>
							<dd>
								{#if !ready}<span class="text-ink-faint">Not read by DeskuBe yet</span>
								{:else}
									{ready.status === 'True' ? 'Yes' : ready.status === 'False' ? 'No' : 'Unknown'}
									{#if ready.reason}<span class="text-ink-muted">({ready.reason})</span>{/if}
									{#if ready.message}<p class="text-xs text-ink-muted">{ready.message}</p>{/if}
								{/if}
							</dd>
						</div>
					</dl>
				</Card>
			</div>

			{@const shown = listed(d, pool.namespace, 'vdiinstances')}
			<Card
				title="Desktops"
				meta={shown ? plural(pool.desktops.length, 'desktop') : undefined}
				flush
			>
				{@const lines = unlistedLines(d.unlisted, here, true)}
				{#if lines.length}
					<div class="px-card py-3"><UnlistedNote {lines} /></div>
				{/if}
				{#if shown}
					<DesktopTable
						rows={joinClaims(pool.desktops, d.claims)}
						data={d}
						projectOf={owners}
						label="Desktops of {pool.name}"
					>
						{#snippet empty()}
							<div class="px-card py-6 text-center text-[13px]">
								<p class="text-ink-muted">No desktop yet.</p>
								<p class="text-ink-faint">
									{st?.missingClass
										? 'None is made until its class exists.'
										: pool.keepWarm
											? `It makes ${plural(pool.keepWarm, 'desktop')} to keep warm.`
											: 'One is made when a person claims a seat.'}
								</p>
							</div>
						{/snippet}
					</DesktopTable>
				{/if}
			</Card>
		{/if}
	{/snippet}
</DesktopsFrame>
