<script lang="ts">
	import { X } from '@lucide/svelte';
	import type { Template } from '#lib/api.js';
	import { libraryLabel } from '#lib/templates.js';
	import { plural } from '#lib/format.js';
	import { projectNS, templateGate } from '#lib/gates.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from '#lib/components/Button.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import StageChip from '#lib/components/StageChip.svelte';

	// The open template beside its table: its facts, its parameters, and what
	// to do with it. oncreate is absent where the caller can make no VM.
	let {
		template: t,
		oncreate,
		onclose,
	}: { template: Template; oncreate?: () => void; onclose: () => void } = $props();

	const id = $props.id();
	// Asked only for the open template: one capability read, not one per row.
	const edit = $derived(
		templateGate(
			t.library,
			projectNS(inventory.projects.find((p) => p.name === t.library) ?? { namespaces: [] }),
		),
	);
	const createOff = $derived(t.error ? 'It does not parse. Fix its file first.' : '');

	// name marks a value that is an object or file name, set in mono.
	type Detail = [label: string, value: string, name?: boolean];
	const named = (label: string, value = ''): Detail => [label, value || '-', !!value];
	const detail = $derived<Detail[]>([
		['Library', libraryLabel(t.library)],
		...(t.description ? [['Description', t.description] as Detail] : []),
		named('Instance type', t.instancetype),
		named('Preference', t.preference),
		named('Boot image', t.image),
		named('File', t.sourceFile),
		...(t.error ? [['Error', t.error] as Detail] : []),
	]);
</script>

<aside
	aria-labelledby={id}
	class="flex min-w-0 flex-[1_1_320px] flex-col rounded-lg border border-line bg-panel [--card-x:18px]"
>
	<div
		class="flex items-start gap-2 rounded-t-[7px] border-b border-line-soft bg-inset px-card py-3"
	>
		<div class="min-w-0 flex-1">
			<h2 {id} class="font-mono text-base leading-6 font-medium break-all">{t.name}</h2>
			<span class="block text-[13px] text-ink-muted"
				>VM template in {libraryLabel(t.library)}{t.error ? '. It does not parse' : ''}.</span
			>
		</div>
		<button
			class="flex size-[30px] shrink-0 items-center justify-center rounded-md text-ink-muted hover:bg-inset-strong"
			aria-label="Close {t.name}"
			onclick={onclose}><X size={16} /></button
		>
	</div>

	<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 px-card py-3.5 text-[13px]">
		{#each detail as [label, value, name] (label)}
			<dt class="text-ink-muted">{label}</dt>
			<dd
				class="min-w-0 break-words {name ? 'font-mono text-xs leading-5' : ''} {label === 'Error'
					? 'text-warn-ink'
					: ''}"
			>
				{value}
			</dd>
		{/each}
	</dl>

	{#if t.parameters?.length}
		<div class="flex flex-col gap-2 border-t border-line-soft px-card py-3.5">
			<h3 class="text-[13px] leading-5 font-bold">
				Asks for {plural(t.parameters.length, 'parameter')}
			</h3>
			<ul class="flex flex-col gap-1.5 text-[13px]">
				{#each t.parameters as p (p.name)}
					<li>
						<div class="flex justify-between gap-3">
							<span class="font-mono text-xs text-ink">{p.name}</span>
							<span class="text-xs text-ink-muted">
								{p.generate
									? 'generated'
									: p.value
										? p.value
										: p.required
											? 'required'
											: 'optional'}
							</span>
						</div>
						{#if p.description}<p class="text-xs leading-4 text-ink-faint">{p.description}</p>{/if}
					</li>
				{/each}
			</ul>
		</div>
	{/if}

	<div class="flex flex-col gap-3.5 border-t border-line-soft px-card py-3.5">
		{#if oncreate}
			<div class="flex flex-col gap-1.5">
				<div class="flex flex-wrap items-center gap-2">
					<Button
						variant="outline"
						size="sm"
						disabled={!!createOff}
						title={createOff}
						onclick={oncreate}>Create VM</Button
					>
					<StageChip kind="pr" />
				</div>
				<Consequence size="sm">
					{createOff ||
						`Opens the create sheet with ${t.name} picked. The VM is made when its pull request merges.`}
				</Consequence>
			</div>
		{/if}
		<div class="flex flex-col gap-1.5">
			<div class="flex flex-wrap items-center gap-2">
				<Button
					variant="secondary"
					size="sm"
					disabled={!edit.ok}
					title={edit.why}
					onclick={() => (ui.modal = { kind: 'editTemplate', template: t })}>Edit template</Button
				>
				<StageChip kind="pr" />
			</div>
			<Consequence size="sm">
				{edit.ok ? `Replaces ${t.sourceFile} in ${libraryLabel(t.library)}.` : edit.why}
			</Consequence>
		</div>
	</div>
</aside>
