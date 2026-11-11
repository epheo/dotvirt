<script lang="ts">
	import { branding } from '#lib/state/branding.svelte.js';
	import type { VM } from '#lib/api.js';
	import { plural } from '#lib/format.js';
	import { clusterObjectGate } from '#lib/gates.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import Row from '#lib/components/Row.svelte';

	// The host's Configure tab: the physical fabric it carries. dotvirt owns
	// nothing here - node configuration is the cluster platform's; the
	// maintenance verbs sit in the page header.
	let { node, vms }: { node: string; vms: VM[] } = $props();

	const nodeUplinks = $derived(inventory.uplinks.filter((u) => !u.nodes || u.nodes.includes(node)));
	const nics = $derived(inventory.physicalAdapters.filter((a) => a.node === node));
	const addUplink = $derived(clusterObjectGate('uplink', 'create'));
</script>

{#snippet newUplink()}
	<Button variant="link" onclick={() => (ui.modal = { kind: 'uplink' })}>Add uplink</Button>
{/snippet}

<div class="grid items-start gap-4 lg:grid-cols-2">
	<div class="flex min-w-0 flex-col gap-4">
		<Card title="Host" flush>
			<dl class="divide-y divide-line-soft text-[13px]">
				<Row label="Name" value={node} mono />
				<Row label="VMs placed here" value={vms.length.toLocaleString('en-US')} />
			</dl>
			<p class="border-t border-line-soft px-card py-2.5 text-xs text-ink-muted">
				Host configuration is managed by the cluster platform, not {branding.name}.
			</p>
		</Card>

		<Card title="Physical adapters" meta={nics.length ? String(nics.length) : undefined} flush>
			{#if !inventory.nmstatePresent}
				<p class="px-card py-3 text-[13px] text-ink-muted">
					Install the NMState operator instance to discover physical adapters.
				</p>
			{:else if nics.length}
				<ul class="divide-y divide-line-soft text-[13px]">
					{#each nics as a (a.name)}
						<li class="flex h-10 items-center justify-between gap-3 px-card">
							<span class="font-mono text-ink">{a.name}</span>
							<span class="flex items-center gap-3 text-right text-ink-muted">
								<span>{a.role}</span>
								<span>{a.state}{a.mtu ? `, MTU ${a.mtu}` : ''}</span>
							</span>
						</li>
					{/each}
				</ul>
			{:else}
				<p class="px-card py-3 text-[13px] text-ink-muted">No physical adapters reported.</p>
			{/if}
		</Card>
	</div>

	{#if nodeUplinks.length}
		<Card
			title="Uplinks"
			meta={String(nodeUplinks.length)}
			actions={addUplink.ok ? newUplink : undefined}
			flush
		>
			<ul class="divide-y divide-line-soft text-[13px]">
				{#each nodeUplinks as u (u.name)}
					<li class="flex h-10 items-center justify-between gap-3 px-card">
						<span class="flex items-baseline gap-2">
							<span class="font-mono text-ink">{u.name}</span>
							{#if u.builtin}<span class="text-xs text-ink-muted">default</span>{/if}
						</span>
						<span class="text-ink-muted"
							><span class="font-mono">{u.bridge}</span>, on {plural(u.nodeCount, 'host')}</span
						>
					</li>
				{/each}
			</ul>
		</Card>
	{/if}
</div>
