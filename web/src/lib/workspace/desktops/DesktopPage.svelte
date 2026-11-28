<script lang="ts">
	import {
		desktopState,
		joinClaims,
		listed,
		person,
		sessionLabel,
		unlistedLines,
	} from '#lib/desktops.js';
	import { relativeAge } from '#lib/format.js';
	import type { Scope } from '#lib/lenses.js';
	import { hrefForScope, trailForScope } from '#lib/nav.js';
	import { desktops } from '#lib/state/desktops.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import Card from '#lib/components/Card.svelte';
	import ClassMissing from './ClassMissing.svelte';
	import DesktopsFrame from './DesktopsFrame.svelte';
	import UnlistedNote from './UnlistedNote.svelte';

	// A desktop made one by one: who it is for, what it runs, how it stands.
	let { scope }: { scope: Extract<Scope, { kind: 'desktop' }> } = $props();

	const data = $derived(desktops.value?.synced ? desktops.value : null);
	const mine = (d: { namespace: string; name: string }) =>
		d.namespace === scope.namespace && d.name === scope.desktop;
	const desktop = $derived(data?.desktops.find(mine));
	// A pool's desktop is a row of its pool, never a page of its own.
	const ofPool = $derived(desktop ? undefined : data?.pools.find((p) => p.desktops.some(mine)));
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
	const ROW =
		'grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-3 border-t border-line-soft py-2 first:border-t-0';
</script>

<DesktopsFrame
	trail={trailForScope(scope, 'Desktops', (p) => inventory.title(p), 'desktops')}
	title={scope.desktop}
	status={st && { tone: st.tone, text: st.label }}
	{context}
>
	{#snippet children(d)}
		{#if ofPool}
			<p class="text-[13px] text-ink-muted">
				{scope.desktop} is a desktop of pool
				<a
					class="text-accent-ink hover:underline"
					href={hrefForScope({
						kind: 'pool',
						project: scope.project,
						namespace: ofPool.namespace,
						pool: ofPool.name,
					})}
				>
					{ofPool.name}
				</a>: it shows in that pool's table.
			</p>
		{:else if !desktop}
			{#if !listed(d, scope.namespace, 'vdiinstances')}
				<UnlistedNote lines={unlistedLines(d.unlisted, here, true)} />
			{:else}
				<p class="text-[13px] text-ink-muted">
					No desktop {scope.desktop} in {scope.namespace}: it was deleted, or is not applied yet.
					<a class="text-accent-ink hover:underline" href={nsHref}>See {scope.namespace}</a>
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
							<dd>{desktop.phase || 'Not read yet'}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Session</dt>
							<dd>{sessionLabel(desktop.session)}</dd>
						</div>
						<div class={ROW}>
							<dt class="text-ink-muted">Running</dt>
							<dd>{desktop.running ? 'Yes' : 'No'}</dd>
						</div>
						{#if row?.claim}
							<div class={ROW}>
								<dt class="text-ink-muted">Claimed by</dt>
								<dd>
									{person(row)}
									{#if row.claim.lastUsed}
										<span class="text-ink-muted">
											, last used {relativeAge(row.claim.lastUsed)}
										</span>
									{/if}
								</dd>
							</div>
						{/if}
					</dl>
				</Card>

				{#if desktop.conditions?.length}
					<Card title="Conditions">
						<ul class="flex flex-col text-[13px]">
							{#each desktop.conditions as c (c.type)}
								<li class="border-t border-line-soft py-2 first:border-t-0">
									<span class="font-medium {c.status === 'False' ? 'text-warn-ink' : 'text-ink'}">
										{c.type}
									</span>
									<span class="text-ink-muted">
										{c.status === 'True' ? 'Yes' : c.status === 'False' ? 'No' : 'Unknown'}{c.reason
											? `, ${c.reason}`
											: ''}
									</span>
									{#if c.message}<p class="text-xs text-ink-muted">{c.message}</p>{/if}
								</li>
							{/each}
						</ul>
					</Card>
				{/if}
			</div>
		{/if}
	{/snippet}
</DesktopsFrame>
