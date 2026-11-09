<script lang="ts">
	import type { StorageClass, StorageClassInfo } from '#lib/api.js';
	import { liveMigrates } from '#lib/create.js';
	import { classPromise } from '#lib/replication.js';
	import { bytes } from '#lib/format.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { LABEL } from './sheet.svelte.js';

	// A storage class picker that says what each class means for a VM: shared
	// (it can live-migrate) or pinned to one host, and the free space when the
	// driver reports it. value '' is the cluster default.
	let {
		label,
		value,
		onpick,
		classes,
	}: {
		label: string;
		value: string;
		onpick: (name: string) => void;
		classes: (StorageClassInfo | StorageClass)[];
	} = $props();

	const id = $props.id();
	const facts = (c: StorageClassInfo | StorageClass) =>
		'provisioner' in c ? (c as StorageClassInfo) : undefined;
</script>

<fieldset class="flex min-w-0 flex-col">
	<legend class="mb-1.5 {LABEL}">{label}</legend>
	<div class="flex flex-col overflow-hidden rounded-md border border-line">
		{#each classes as c (c.name)}
			{@const info = facts(c)}
			{@const on = (value || inventory.defaultStorageClass) === c.name}
			<label
				class="grid cursor-pointer grid-cols-[16px_minmax(0,1fr)] gap-3 border-t border-line-soft px-3 py-2 first:border-t-0 {on
					? 'bg-select-soft'
					: 'hover:bg-inset'}"
			>
				<input
					type="radio"
					name={id}
					checked={on}
					onchange={() => onpick(c.default ? '' : c.name)}
					class="mt-0.5 size-4 accent-accent"
				/>
				<span class="min-w-0">
					<span class="flex items-baseline justify-between gap-3">
						<span class="font-mono text-sm font-medium text-ink"
							>{c.name}{#if c.default}<span
									class="ml-1.5 font-sans text-xs font-normal text-ink-faint">default</span
								>{/if}</span
						>
						{#if info?.free}<span class="text-[13px] text-ink-soft">{bytes(info.free)} free</span
							>{/if}
					</span>
					{#if info?.replication}
						{@const promise = classPromise(info.replication)}
						<span class="flex items-center gap-1.5 text-xs text-ink-muted">
							<span class="size-[7px] rounded-full {promise.ok ? 'bg-ok' : 'bg-warn'}"></span>
							{promise.text}
						</span>
					{/if}
					{#if info}
						<span class="flex items-center gap-1.5 text-xs text-ink-muted">
							<span class="size-[7px] rounded-full {liveMigrates(info) ? 'bg-ok' : 'bg-warn'}"
							></span>
							{liveMigrates(info)
								? 'Shared across hosts: live migration possible'
								: 'Stays on one host: no live migration'}
						</span>
					{/if}
				</span>
			</label>
		{/each}
	</div>
</fieldset>
