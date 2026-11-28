<script lang="ts">
	import {
		desktopState,
		joinClaims,
		person,
		sessionLabel,
		stateLine,
		unlistedLines,
		why,
	} from '#lib/desktops.js';
	import { relativeAge } from '#lib/format.js';
	import type { Scope } from '#lib/lenses.js';
	import { hrefForScope, trailForScope } from '#lib/nav.js';
	import { desktops } from '#lib/state/desktops.svelte.js';
	import Card from '#lib/components/Card.svelte';
	import ClassMissing from './ClassMissing.svelte';
	import DesktopActions from './DesktopActions.svelte';
	import DesktopsFrame from './DesktopsFrame.svelte';
	import UnlistedNote from './UnlistedNote.svelte';

	// A desktop made one by one: who it is for, what it runs, how it stands,
	// and its acts.
	let { scope }: { scope: Extract<Scope, { kind: 'desktop' }> } = $props();

	const data = $derived(desktops.value?.synced ? desktops.value : null);
	const may = $derived(data ? desktops.may : null);
	const mine = (d: { namespace: string; name: string }) =>
		d.namespace === scope.namespace && d.name === scope.desktop;
	// A pool's desktop is a row of its pool, never a page of its own, even
	// where its pool is not shown.
	const desktop = $derived(data?.desktops.find((d) => mine(d) && !d.pool));
	const ofPool = $derived(
		desktop
			? undefined
			: (data?.desktops.find(mine)?.pool ?? data?.pools.find((p) => p.desktops.some(mine))?.name),
	);
	const poolShown = $derived(
		!!ofPool && !!data?.pools.some((p) => p.namespace === scope.namespace && p.name === ofPool),
	);
	const row = $derived(desktop && data ? joinClaims([desktop], data.claims)[0] : undefined);
	const st = $derived(desktop && desktopState(desktop));
	const cls = $derived(desktop?.class && data?.classes.find((c) => c.name === desktop.class));
	const here = $derived(new Set([scope.namespace]));
	const nsHref = $derived(
		hrefForScope(
			{ kind: 'namespace', project: scope.project, namespace: scope.namespace },
			'desktops',
		),
	);

	const context = $derived.by(() => {
		if (!desktop) return '';
		const of = desktop.owner ? `Desktop of ${desktop.owner}` : 'Desktop';
		return desktop.class ? `${of}, class ${desktop.class}` : of;
	});
	const claimedBy = $derived(
		row?.claim
			? [person(row), row.claim.lastUsed ? `last used ${relativeAge(row.claim.lastUsed)}` : '']
					.filter(Boolean)
					.join(', ')
			: '',
	);
	const ROW =
		'grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-3 border-t border-line-soft py-2 first:border-t-0';
</script>

{#snippet acts()}
	{#if row}<DesktopActions {row} />{/if}
{/snippet}

<DesktopsFrame
	trail={trailForScope(scope, 'Desktops', (p) => desktops.title(p), 'desktops')}
	title={scope.desktop}
	status={st && { tone: st.tone, text: st.label }}
	{context}
	actions={row ? acts : undefined}
>
	{#snippet children(d)}
		{#if ofPool}
			<p class="text-[13px] text-ink-muted">
				{scope.desktop} is a desktop of pool
				{#if poolShown}
					<a
						class="text-accent-ink hover:underline"
						href={hrefForScope({
							kind: 'pool',
							project: scope.project,
							namespace: scope.namespace,
							pool: ofPool,
						})}
					>
						{ofPool}
					</a>: it shows in that pool's table.
				{:else}
					{ofPool}, which is not shown to you.
				{/if}
			</p>
		{:else if !desktop}
			{@const lines = unlistedLines(d.unlisted, here, true)}
			{#if may?.lists(scope.namespace, 'vdiinstances')}
				<p class="text-[13px] text-ink-muted">
					No desktop {scope.desktop} in {scope.namespace}: it was deleted, or is not applied yet.
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
					outcome="this desktop does not start"
					project={scope.project}
					classes={d.classes}
				/>
			{:else if st?.message}
				<p
					class="rounded-lg border border-danger/30 bg-danger-soft px-4 py-3 text-[13px] text-danger-ink"
				>
					DeskuBe reports: {st.message}
				</p>
			{/if}

			<div class="grid grid-cols-[repeat(auto-fit,minmax(min(360px,100%),1fr))] items-start gap-4">
				<Card title="Desktop">
					<dl class="text-[13px]">
						<div class={ROW}>
							<dt class="text-ink-muted">Owner</dt>
							<dd>{desktop.owner || 'No one'}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Class</dt>
							<dd class="font-mono text-xs">{desktop.class || '-'}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Image</dt>
							<dd class="font-mono text-xs break-all">
								{#if desktop.image}{desktop.image}
								{:else if cls}{cls.image}
									<span class="font-sans text-ink-faint">(its class's)</span>
								{:else}<span class="font-sans text-ink-faint">Its class's</span>{/if}
							</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Phase</dt>
							<dd>{stateLine({ label: desktop.phase || 'Not read yet', note: st?.note })}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Session</dt>
							<dd>{sessionLabel(desktop.session)}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Running</dt>
							<dd>{desktop.running ? 'Yes' : 'No'}</dd>
						</div>
						{#if claimedBy}
							<div class={ROW}>
								<dt class="text-ink-muted">Claimed by</dt>
								<dd>{claimedBy}</dd>
							</div>
						{/if}
					</dl>
				</Card>

				{#if desktop.conditions?.length}
					<Card title="Conditions">
						<dl class="text-[13px]">
							{#each desktop.conditions as c (c.type)}
								<div class={ROW}>
									<dt class="text-ink-muted">{c.type}</dt>
									<dd class={c.status === 'False' ? 'text-warn-ink' : ''}>
										{c.status === 'True'
											? 'Yes'
											: c.status === 'False'
												? `No: ${why(c, desktop.class)}`
												: 'Unknown'}
									</dd>
								</div>
							{/each}
						</dl>
					</Card>
				{/if}
			</div>
		{/if}
	{/snippet}
</DesktopsFrame>
