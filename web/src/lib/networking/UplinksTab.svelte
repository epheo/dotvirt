<script lang="ts">
	import { CircleX, Plug, Plus } from '@lucide/svelte';
	import type { Uplink } from '#lib/api.js';
	import { uplinkLabel } from '#lib/netmap.js';
	import { networkHref } from '#lib/nav.js';
	import { networkKey } from '#lib/networks.js';
	import {
		canAdoptUplink,
		canEditUplink,
		openAdopt,
		openDelete,
		openEdit,
		uplinkRef,
	} from '#lib/objects.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { networking } from '#lib/state/networking.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import StageChip from '#lib/components/StageChip.svelte';
	import SyncBadge from '#lib/components/SyncBadge.svelte';

	// The Uplinks tab: how hosts reach the physical fabric. Each uplink with its
	// bridge, its hosts, the VLAN networks riding it and, where it failed on a
	// host, nmstate's reason and the fix.
	const hostTotal = $derived(inventory.uplinks.find((u) => u.builtin)?.nodeCount ?? 0);
	const vlansOn = (u: Uplink) => networking.vlanNets.filter((n) => n.uplink === u.name);
	const fmt = (n: number) => n.toLocaleString('en-US');
</script>

<div class="flex flex-col gap-4">
	{#if inventory.caps?.uplink}
		<div class="flex">
			<Button variant="outline" class="ml-auto" onclick={() => (ui.modal = { kind: 'uplink' })}
				><Plus size={16} />New uplink</Button
			>
		</div>
	{/if}
	{#if !inventory.uplinks.length}
		<Card title="Uplinks">
			<p class="text-[13px] text-ink-faint">
				{inventory.nmstatePresent
					? 'Uplinks are host configuration: platform admins see them here.'
					: 'Uplinks need the NMState operator on the cluster.'}
			</p>
		</Card>
	{/if}
	{#each inventory.uplinks as u (u.name)}
		{@const vlans = vlansOn(u)}
		{@const failing = u.failures ?? []}
		<Card title={uplinkLabel(u)} meta={u.bridge}>
			{#snippet actions()}
				{#if u.sync && u.sync !== 'Synced'}<SyncBadge
						sync={u.sync}
						error={u.syncError ?? ''}
						compact
					/>{/if}
				{#if canAdoptUplink(u)}
					<button
						type="button"
						class="text-accent-ink hover:underline"
						onclick={() => openAdopt(uplinkRef(u))}>Adopt into git</button
					>
				{:else if canEditUplink(u)}
					<button
						type="button"
						class="text-accent-ink hover:underline"
						onclick={() => openEdit(uplinkRef(u))}>Edit</button
					>
					<button
						type="button"
						class="text-danger-ink hover:underline"
						onclick={() => openDelete(uplinkRef(u), u.sourceFile!)}>Delete</button
					>
				{/if}
			{/snippet}
			<div class="flex flex-col gap-3">
				<div class="flex flex-wrap items-center gap-2 text-[13px] text-ink-soft">
					{#if failing.length}<CircleX size={16} class="text-danger" />{:else}<Plug
							size={16}
							class="text-ink-faint"
						/>{/if}
					<span
						>On {fmt(u.nodeCount)}{hostTotal ? ` of ${fmt(hostTotal)}` : ''} host{u.nodeCount === 1
							? ''
							: 's'}{u.builtin ? '. Pod, Shared and project networks leave here.' : '.'}</span
					>
					{#if u.policy}<span class="font-mono text-xs text-ink-faint">{u.policy}</span>{/if}
				</div>
				{#if u.nodes?.length && !u.builtin}
					<div class="flex flex-wrap gap-1">
						{#each u.nodes.slice(0, 16) as h (h)}
							<span
								class="rounded px-1.5 py-0.5 font-mono text-xs {failing.some((f) => f.node === h)
									? 'bg-danger-soft text-danger-ink'
									: 'bg-inset text-ink-soft'}">{h}</span
							>
						{/each}
						{#if u.nodes.length > 16}<span class="px-1 text-xs text-ink-faint"
								>{u.nodes.length - 16} more</span
							>{/if}
					</div>
				{/if}
				{#if vlans.length}
					<p class="text-[13px] text-ink-soft">
						Carries
						{#each vlans.slice(0, 6) as n, i (networkKey(n))}{i ? ', ' : ''}<a
								href={networkHref(networkKey(n))}
								class="font-mono text-accent-ink underline underline-offset-2">{n.name}</a
							>{/each}{vlans.length > 6 ? ` and ${vlans.length - 6} more` : ''}.
					</p>
				{/if}
				{#each failing as f (f.node)}
					<div
						class="flex flex-col gap-2 rounded-md border border-danger/40 bg-danger-soft px-3 py-2.5"
					>
						<p class="text-[13px] text-danger-ink">
							Not configured on <span class="font-mono">{f.node}</span>:
							<span class="font-mono">{f.reason}</span>
						</p>
						{#if f.message}<p class="font-mono text-[11px] break-all text-danger-ink/80">
								{f.message}
							</p>{/if}
						{#if canEditUplink(u)}
							<div class="flex flex-wrap items-center gap-2">
								<Button variant="outline" size="sm" onclick={() => openEdit(uplinkRef(u))}
									>Edit {uplinkLabel(u)}</Button
								>
								<StageChip kind="pr" />
							</div>
							<Consequence size="sm" tone="info"
								>Merging a fix makes nmstate retry it on {f.node}.</Consequence
							>
						{/if}
						{#if vlans.length}
							<Consequence size="sm" tone="danger"
								>VMs on {vlans.map((n) => n.name).join(', ')} that run on {f.node} have no network there.</Consequence
							>
						{/if}
					</div>
				{/each}
			</div>
		</Card>
	{/each}
</div>
