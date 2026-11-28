<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { Desktops } from '#lib/api.js';
	import { desktopState, person, sessionLabel, type DesktopRow } from '#lib/desktops.js';
	import { relativeAge } from '#lib/format.js';
	import { hrefForScope } from '#lib/nav.js';
	import { desktops } from '#lib/state/desktops.svelte.js';
	import { TONE_TEXT } from '#lib/status.js';
	import StatusDot from '#lib/components/StatusDot.svelte';
	import DesktopActions from './DesktopActions.svelte';

	// Desktops with the claims holding them. A desktop made one by one opens
	// its page, where it is acted on; a pool's desktop is a row only, its pool
	// the page, and its acts are the row's.
	let {
		rows,
		data,
		namespace = false,
		pool = false,
		label,
		empty,
	}: {
		rows: DesktopRow[];
		data: Desktops;
		namespace?: boolean;
		pool?: boolean;
		label: string;
		empty?: Snippet;
	} = $props();

	const projectOf = $derived(desktops.owners);
	const may = $derived(desktops.may);
	const pools = $derived(new Set(data.pools.map((p) => `${p.namespace}/${p.name}`)));
	const acts = $derived(rows.some((r) => r.desktop.pool));
	const TH =
		'border-b border-line px-3 py-2 text-left text-xs font-bold whitespace-nowrap text-ink-muted';
	const TD = 'border-b border-line-soft px-3 py-2.5 whitespace-nowrap';
	const LINK = 'text-accent-ink hover:underline';
</script>

{#if rows.length === 0 && empty}
	{@render empty()}
{:else}
	<div class="overflow-x-auto">
		<table class="w-full border-collapse text-[13px] tabular-nums" aria-label={label}>
			<thead>
				<tr>
					<th scope="col" class="{TH} pl-card">Desktop</th>
					{#if namespace}<th scope="col" class={TH}>Namespace</th>{/if}
					{#if pool}<th scope="col" class={TH}>Pool</th>{/if}
					<th scope="col" class={TH}>Person</th>
					<th scope="col" class={TH}>Phase</th>
					<th scope="col" class={TH}>Session</th>
					<th scope="col" class={TH}>Running</th>
					<th scope="col" class="{TH} {acts ? '' : 'pr-card'}">Last used</th>
					{#if acts}
						<th scope="col" class="{TH} w-px pr-card"><span class="sr-only">Actions</span></th>
					{/if}
				</tr>
			</thead>
			<tbody>
				{#each rows as r (`${r.desktop.namespace}/${r.desktop.name}`)}
					{@const d = r.desktop}
					{@const project = projectOf.get(d.namespace)}
					{@const st = desktopState(d)}
					{@const who = person(r)}
					<tr class="hover:bg-select-soft">
						<td class="{TD} pl-card font-mono text-xs font-medium">
							{#if project && !d.pool}
								<a
									class={LINK}
									href={hrefForScope({
										kind: 'desktop',
										project,
										namespace: d.namespace,
										desktop: d.name,
									})}
								>
									{d.name}
								</a>
							{:else}{d.name}{/if}
						</td>
						{#if namespace}
							<td class={TD}>
								{#if project}
									<a
										class={LINK}
										href={hrefForScope(
											{ kind: 'namespace', project, namespace: d.namespace },
											'desktops',
										)}
									>
										{d.namespace}
									</a>
								{:else}{d.namespace}{/if}
							</td>
						{/if}
						{#if pool}
							<td class={TD}>
								{#if !d.pool}
									<span class="text-ink-faint">One by one</span>
								{:else if project && pools.has(`${d.namespace}/${d.pool}`)}
									<a
										class={LINK}
										href={hrefForScope({
											kind: 'pool',
											project,
											namespace: d.namespace,
											pool: d.pool,
										})}
									>
										{d.pool}
									</a>
								{:else}{d.pool}{/if}
							</td>
						{/if}
						<td class={TD}>
							{#if who}{who}
							{:else if d.pool && may?.lists(d.namespace, 'vdiclaims')}
								<span class="text-ink-faint">Warm</span>
							{:else}<span class="text-ink-faint">-</span>{/if}
						</td>
						<td class={TD} title={st.message}>
							<span class="inline-flex items-center gap-1.5 {TONE_TEXT[st.tone]}">
								<StatusDot tone={st.tone} />{st.label}
							</span>
							{#if st.note}<span class="text-ink-muted">({st.note})</span>{/if}
						</td>
						<td class={TD}>{sessionLabel(d.session)}</td>
						<td class={TD}>{d.running ? 'Yes' : 'No'}</td>
						<td class="{TD} {acts ? '' : 'pr-card'}">
							{#if r.claim?.lastUsed}{relativeAge(r.claim.lastUsed)}
							{:else}<span class="text-ink-faint">-</span>{/if}
						</td>
						{#if acts}
							<td class="{TD} py-1 pr-card">
								{#if d.pool}<DesktopActions row={r} />{/if}
							</td>
						{/if}
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}
