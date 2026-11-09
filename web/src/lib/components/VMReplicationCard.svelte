<script lang="ts">
	import { untrack } from 'svelte';
	import type { VM } from '#lib/api.js';
	import { clusterGate } from '#lib/gates.js';
	import { storageHref } from '#lib/nav.js';
	import {
		cellFor,
		copySquares,
		diskNow,
		hostLosses,
		hostsOf,
		standingOf,
		vmRow,
	} from '#lib/replication.js';
	import { CHECKING } from '#lib/state/caps.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { replication, replicationOff } from '#lib/state/replication.svelte.js';
	import { size } from '#lib/storage.js';
	import { TONE_TEXT, type Tone } from '#lib/status.js';
	import CopyGlyph from '#lib/workspace/storage/CopyGlyph.svelte';
	import Card from './Card.svelte';
	import StatusPill from './StatusPill.svelte';

	// A VM's replicated disks, on the capability the server asks: an operator
	// sees which host holds what, anyone else how safe each disk is. A VM with
	// none shows nothing.
	let { vm }: { vm: VM } = $props();

	const gate = $derived(clusterGate.replication());
	const deciding = $derived(!gate.ok && gate.why === CHECKING);
	const off = $derived(replicationOff());

	$effect(() => {
		void inventory.replicationVersion;
		if (deciding || off) return;
		const operator = gate.ok;
		untrack(() => replication.pull(operator));
	});

	const row = $derived(
		gate.ok && replication.overview ? vmRow(vm, replication.overview.volumes) : null,
	);
	const hosts = $derived(
		row
			? hostsOf(
					row.disks.map((d) => d.volume),
					[],
					[row.runsOn],
				)
			: [],
	);
	const losses = $derived(row ? hostLosses(row) : []);

	const claims = $derived(
		new Map((vm.disks ?? []).filter((d) => d.claim).map((d) => [d.claim, d.name])),
	);
	const own = $derived(
		gate.ok
			? []
			: (replication.protection ?? [])
					.filter((p) => p.namespace === vm.namespace && claims.has(p.claim))
					.map((p) => ({ p, label: claims.get(p.claim) ?? p.claim, s: standingOf(p) })),
	);

	const LOSS: Record<string, Tone> = { off: 'danger', weak: 'warn', restart: 'info' };
</script>

{#if !off && (row || own.length)}
	<Card
		title="Replicated disks"
		link={row ? { label: 'Where every copy lives', href: storageHref('replication') } : undefined}
	>
		<div class="flex flex-col gap-3">
			{#if row}
				{#each row.disks as d (d.volume.name)}
					{@const now = diskNow(d.volume, row.runsOn)}
					<div class="flex flex-col gap-1.5">
						<div class="flex items-baseline justify-between gap-2">
							<span class="font-mono text-[13px] font-medium text-ink">{d.label}</span>
							<span class="text-xs text-ink-faint">{size(d.volume.sizeBytes)}, {d.volume.tier}</span
							>
						</div>
						<div class="flex flex-wrap gap-x-3 gap-y-1">
							{#each hosts as h (h)}
								{@const c = cellFor(d.volume, h, row.runsOn)}
								<span class="flex flex-col items-center gap-0.5" title={c.label}>
									<span class="flex size-4 items-center justify-center">
										{#if c.kind}
											<CopyGlyph kind={c.kind} percent={c.percent} label={c.label} />
										{:else}
											<span class="block size-1 rounded-full bg-line-strong"></span>
										{/if}
									</span>
									<span
										class="font-mono text-[10px] {h === row.runsOn
											? 'font-bold text-accent-ink'
											: 'text-ink-faint'}"
									>
										{h}
									</span>
								</span>
							{/each}
						</div>
						<span class="text-xs {now.tone === 'neutral' ? 'text-ink-muted' : TONE_TEXT[now.tone]}">
							{now.text}
						</span>
					</div>
				{/each}
				<div class="flex flex-col gap-1 border-t border-line-soft pt-2.5">
					<h3 class="text-xs font-bold text-ink-muted">If a host goes down</h3>
					{#each losses as l (l.host)}
						<div class="flex items-baseline justify-between gap-3 text-xs">
							<span class="font-mono text-ink">{l.host}</span>
							<span class={TONE_TEXT[LOSS[l.fate]]}>{l.text}</span>
						</div>
					{:else}
						<span class="text-xs text-ink-muted">No single host would touch it.</span>
					{/each}
				</div>
			{:else}
				{#each own as { p, label, s } (p.claim)}
					<div class="flex flex-col items-start gap-1">
						<div class="flex w-full items-center justify-between gap-2">
							<span class="font-mono text-[13px] font-medium text-ink">{label}</span>
							<span
								class="flex gap-1"
								role="img"
								aria-label="{p.inSync} of {p.copies} {p.copies === 1 ? 'copy' : 'copies'} in sync"
							>
								{#each copySquares(p) as c, i (i)}<CopyGlyph
										kind={c.kind}
										percent={c.percent}
									/>{/each}
							</span>
						</div>
						<StatusPill tone={s.tone} label={s.label} />
						<span class="text-xs text-ink-muted">{s.detail}</span>
					</div>
				{/each}
			{/if}
		</div>
	</Card>
{/if}
