<script lang="ts">
	import type { DesktopPool } from '#lib/api.js';
	import { assignment, count, poolState } from '#lib/desktops.js';
	import { hrefForScope } from '#lib/nav.js';
	import StatusPill from '#lib/components/StatusPill.svelte';

	// Pools and how their seats stand, each opening its page.
	let {
		pools,
		projectOf,
		namespace = false,
	}: { pools: DesktopPool[]; projectOf: Map<string, string>; namespace?: boolean } = $props();

	const COUNTS = [
		['held', 'Held'],
		['warm', 'Warm'],
		['free', 'Free'],
		['waiting', 'Waiting'],
		['inSession', 'In session'],
	] as const;
	const TH =
		'border-b border-line px-3 py-2 text-left text-xs font-bold whitespace-nowrap text-ink-muted';
	const TD = 'border-b border-line-soft px-3 py-2.5 whitespace-nowrap';
</script>

<div class="overflow-x-auto">
	<table class="w-full border-collapse text-[13px] tabular-nums" aria-label="Pools">
		<thead>
			<tr>
				<th scope="col" class="{TH} pl-card">Pool</th>
				{#if namespace}<th scope="col" class={TH}>Namespace</th>{/if}
				<th scope="col" class={TH}>Class</th>
				<th scope="col" class={TH}>Assignment</th>
				<th scope="col" class="{TH} text-right">Seats</th>
				{#each COUNTS as [k, label] (k)}
					<th scope="col" class="{TH} text-right">{label}</th>
				{/each}
				<th scope="col" class="{TH} pr-card">Status</th>
			</tr>
		</thead>
		<tbody>
			{#each pools as p (`${p.namespace}/${p.name}`)}
				{@const project = projectOf.get(p.namespace)}
				{@const st = poolState(p)}
				<tr class="hover:bg-select-soft">
					<td class="{TD} pl-card font-mono text-xs font-medium">
						{#if project}
							<a
								class="text-accent-ink hover:underline"
								href={hrefForScope({ kind: 'pool', project, namespace: p.namespace, pool: p.name })}
							>
								{p.name}
							</a>
						{:else}{p.name}{/if}
					</td>
					{#if namespace}<td class={TD}>{p.namespace}</td>{/if}
					<td class="{TD} font-mono text-xs">{p.class}</td>
					<td class={TD}>{assignment(p)}</td>
					<td class="{TD} text-right">{p.seats}</td>
					{#each COUNTS as [k] (k)}
						<td class="{TD} text-right">{count(p, k)}</td>
					{/each}
					<td class="{TD} pr-card"><StatusPill tone={st.tone} label={st.label} /></td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>
