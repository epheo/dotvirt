<script lang="ts">
	import {
		assignment,
		count,
		duration,
		joinClaims,
		poolState,
		readyOf,
		seatParts,
		unlistedLines,
		why,
	} from '#lib/desktops.js';
	import { plural } from '#lib/format.js';
	import type { Scope } from '#lib/lenses.js';
	import { hrefForScope, trailForScope } from '#lib/nav.js';
	import { desktops } from '#lib/state/desktops.svelte.js';
	import Card from '#lib/components/Card.svelte';
	import ClassMissing from './ClassMissing.svelte';
	import DesktopTable from './DesktopTable.svelte';
	import DesktopsFrame from './DesktopsFrame.svelte';
	import RepoLink from './RepoLink.svelte';
	import UnlistedNote from './UnlistedNote.svelte';

	// One pool: how its seats stand, its rules, whether DeskuBe can make its
	// desktops, and its desktops with the people holding them.
	let { scope }: { scope: Extract<Scope, { kind: 'pool' }> } = $props();

	const data = $derived(desktops.value?.synced ? desktops.value : null);
	const may = $derived(data ? desktops.may : null);
	const pool = $derived(
		data?.pools.find((p) => p.namespace === scope.namespace && p.name === scope.pool),
	);
	const st = $derived(pool && poolState(pool));
	const ready = $derived(pool && readyOf(pool.conditions));
	const personal = $derived(!!pool && assignment(pool) === 'Personal');
	const parts = $derived(pool && seatParts(pool));
	const here = $derived(new Set([scope.namespace]));
	const nsHref = $derived(
		hrefForScope(
			{ kind: 'namespace', project: scope.project, namespace: scope.namespace },
			'desktops',
		),
	);

	const PARTS = [
		{
			key: 'inSession',
			label: 'In session',
			title: 'Desktops a client streams from, a warm one included',
		},
		{
			key: 'idle',
			label: 'Held, no session',
			title: 'Held seats without a session: running, starting, or with no desktop yet',
		},
		{ key: 'stopped', label: 'Stopped', title: 'Held desktops their claim stopped' },
		{ key: 'warm', label: 'Warm', title: 'Running desktops no claim holds yet' },
		{ key: 'free', label: 'Free', title: 'Seats without a desktop' },
	] as const;
	const ROW =
		'grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-3 border-t border-line-soft py-2 first:border-t-0';
	const STAT = 'text-xl font-bold text-ink tabular-nums';
</script>

<DesktopsFrame
	trail={trailForScope(scope, 'Desktops', (p) => desktops.title(p), 'desktops')}
	title={scope.pool}
	status={st && { tone: st.tone, text: st.label }}
	context={pool
		? `${assignment(pool)} pool of class ${pool.class} in ${pool.namespace}${pool.closed ? ', closed' : ''}`
		: ''}
>
	{#snippet children(d)}
		{#if !pool}
			{@const lines = unlistedLines(d.unlisted, here, true)}
			{#if may?.lists(scope.namespace, 'vdipools')}
				<p class="text-[13px] text-ink-muted">
					No pool {scope.pool} in {scope.namespace}: it was deleted, or is not applied yet.
					<a class="text-accent-ink hover:underline" href={nsHref}>See {scope.namespace}</a>
				</p>
			{:else if lines.length}
				<UnlistedNote {lines} />
			{:else}
				<p class="text-[13px] text-ink-muted">
					{scope.namespace} is not a namespace of your projects with a repo.
					<a class="text-accent-ink hover:underline" href="/desktops">See all desktops</a>
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
			{/if}
			{#if pool.closed}
				<p class="rounded-lg border border-line bg-inset px-4 py-3 text-[13px] text-ink-soft">
					Closed: claims without a desktop wait, and none is kept warm. Reopen it in
					<RepoLink project={scope.project} />.
				</p>
			{/if}

			<div class="grid grid-cols-[repeat(auto-fit,minmax(min(360px,100%),1fr))] items-start gap-4">
				<Card title="Seats" meta="{count(pool, 'held')} of {pool.seats} held">
					<!-- The parts never overlap: they add up to held, warm and free. -->
					<dl class="grid grid-cols-5 gap-x-4 gap-y-3">
						{#each PARTS as s (s.key)}
							<div title={s.title}>
								<dt class="text-xs text-ink-muted">{s.label}</dt>
								<dd class={STAT}>{parts?.[s.key]}</dd>
							</div>
						{/each}
					</dl>
					<dl class="mt-3 grid grid-cols-5 gap-x-4 border-t border-line-soft pt-3">
						<div>
							<dt class="text-xs text-ink-muted">Seats</dt>
							<dd class={STAT}>{pool.seats}</dd>
						</div>
						<div title="Claims with no seat yet">
							<dt class="text-xs text-ink-muted">Waiting</dt>
							<dd class={STAT}>{count(pool, 'waiting')}</dd>
						</div>
					</dl>
					{#if count(pool, 'waiting') && !st?.missingClass}
						<p class="mt-3 text-[13px] text-ink-muted">
							{#if pool.closed}
								{plural(count(pool, 'waiting'), 'claim')} waiting: claims wait until the pool is reopened
								in <RepoLink project={scope.project} />.
							{:else}
								{plural(count(pool, 'waiting'), 'claim')} waiting for a seat: raise the seats in
								<RepoLink project={scope.project} /> to seat them.
							{/if}
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
								{personal
									? 'Personal: each person keeps a desktop and its home'
									: 'Shared: any free desktop, given back when its person leaves'}
							</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Who may claim</dt>
							<dd>
								{#if pool.subjects?.length}
									{pool.subjects.map((s) => `${s.kind} ${s.name}`).join(', ')}
								{:else}
									No one named: rights granted elsewhere decide
								{/if}
							</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Closed</dt>
							<dd>{pool.closed ? 'Yes: claims without a desktop wait' : 'No'}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Keeps warm</dt>
							<dd>
								{pool.closed
									? 'None, closed'
									: pool.keepWarm
										? plural(pool.keepWarm, 'desktop')
										: 'None'}
							</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Idle timeout</dt>
							<dd>
								{#if !pool.idleTimeout}The class's
								{:else if pool.idleTimeout === '0s'}Never
								{:else}
									{personal ? 'Stopped' : 'Given back'} after {duration(pool.idleTimeout)} without a session
								{/if}
							</dd>
						</div>
						{#if personal}
							<div class={ROW}>
								<dt class="text-ink-muted">Release after</dt>
								<dd>
									{#if !pool.releaseAfter || pool.releaseAfter === '0s'}Never
									{:else}
										Claim, desktop and home deleted after {duration(pool.releaseAfter)} without a session
									{/if}
								</dd>
							</div>
						{/if}
						<div class={ROW}>
							<dt class="text-ink-muted">Ready</dt>
							<dd>
								{#if !ready}<span class="text-ink-faint">Not read by DeskuBe yet</span>
								{:else if ready.status === 'True'}Yes
								{:else if ready.status === 'False'}No: {why(ready, pool.class)}
								{:else}Unknown{/if}
							</dd>
						</div>
					</dl>
				</Card>
			</div>

			{@const shown = may?.lists(pool.namespace, 'vdiinstances') ?? false}
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
						label="Desktops of {pool.name}"
					>
						{#snippet empty()}
							<div class="px-card py-6 text-center text-[13px]">
								<p class="text-ink-muted">No desktop yet.</p>
								<p class="text-ink-faint">
									{st?.missingClass
										? 'None is made until its class exists.'
										: pool.closed
											? 'A closed pool keeps none warm.'
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
