<script lang="ts">
	import { page } from '$app/state';
	import type { DiskProtection } from '#lib/api.js';
	import { vmHref } from '#lib/nav.js';
	import { copySquares, standingOf } from '#lib/replication.js';
	import type { DiskRow } from '#lib/storage.js';
	import Card from '#lib/components/Card.svelte';
	import StatusPill from '#lib/components/StatusPill.svelte';
	import CopyGlyph from './CopyGlyph.svelte';

	// A disk owner's view: how safe each of their disks is and what to do about
	// it. The server's rows name no host, and neither does anything here.
	let {
		protection,
		rows,
		project,
		namespaces,
	}: {
		protection: DiskProtection[];
		rows: DiskRow[];
		project: string;
		namespaces: Set<string>;
	} = $props();

	const owner = $derived(
		new Map(rows.filter((d) => d.disk.claim).map((d) => [`${d.vm.namespace}/${d.disk.claim}`, d])),
	);
	// By namespace: a claim no VM holds still belongs to its project.
	const list = $derived(
		protection
			.filter((p) => !project || namespaces.has(p.namespace))
			.map((p) => ({ p, d: owner.get(`${p.namespace}/${p.claim}`), s: standingOf(p) })),
	);
	const safe = $derived(list.filter((x) => x.p.standing === 'protected').length);
	// The disk a landing tile sent the reader to.
	const focus = $derived(page.url.searchParams.get('focus') ?? '');
</script>

{#if list.length === 0}
	<p class="text-sm text-ink-muted">
		None of your disks{project ? ' in this project' : ''} use replicated storage. Pick a replicated class
		when you add a disk.
	</p>
{:else}
	<Card title="Your replicated disks" meta="{safe} of {list.length} survive losing a host" flush>
		<ul>
			{#each list as { p, d, s } (`${p.namespace}/${p.claim}`)}
				{@const focused = `${p.namespace}/${p.claim}` === focus}
				<li
					class="flex flex-wrap items-center gap-x-5 gap-y-1.5 border-b border-line-soft px-card py-3 last:border-b-0 {focused
						? 'bg-select-soft'
						: ''}"
					aria-current={focused ? 'true' : undefined}
					{@attach focused && ((el) => el.scrollIntoView({ block: 'center' }))}
				>
					<div class="flex min-w-[220px] flex-1 flex-col">
						{#if d}
							<a
								class="font-bold text-accent-ink hover:underline"
								href={vmHref(d.vm.namespace, d.vm.name)}
							>
								{d.vm.name}
							</a>
							<span class="text-xs text-ink-faint">{d.disk.name}, {p.namespace}</span>
						{:else}
							<span class="font-bold text-ink">{p.claim}</span>
							<span class="text-xs text-ink-faint">{p.namespace}</span>
						{/if}
					</div>
					<span
						class="flex w-[76px] gap-1"
						role="img"
						aria-label="{p.inSync} of {p.copies} {p.copies === 1 ? 'copy' : 'copies'} in sync"
					>
						{#each copySquares(p) as c, i (i)}<CopyGlyph kind={c.kind} percent={c.percent} />{/each}
					</span>
					<div class="flex min-w-[260px] flex-[1.4] flex-col items-start gap-0.5">
						<StatusPill tone={s.tone} label={s.label} />
						<span class="text-[13px] text-ink-muted">{s.detail}</span>
					</div>
				</li>
			{/each}
		</ul>
	</Card>
{/if}
