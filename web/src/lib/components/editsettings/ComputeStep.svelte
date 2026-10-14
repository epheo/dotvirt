<script lang="ts">
	import type { Instancetype, Options, VM } from '#lib/api.js';
	import type { EditForm } from '#lib/editform.js';
	import FormField from '../FormField.svelte';
	import Note from '../Note.svelte';
	import SelectInput from '../SelectInput.svelte';
	import TextInput from '../TextInput.svelte';

	let {
		form = $bindable(),
		vm,
		options,
		optionsError,
		selectedIT,
		prefViolation,
	}: {
		form: EditForm;
		vm: VM;
		options: Options | null;
		optionsError: string;
		selectedIT: Instancetype | undefined;
		prefViolation: string;
	} = $props();

	function setMode(next: 'instancetype' | 'custom') {
		if (next === form.mode) return;
		// Converting to custom: start from the instancetype's numbers so the user
		// tweaks real values rather than the blank inline fields.
		if (next === 'custom' && !form.cpuCores && !form.memory && selectedIT) {
			form.cpuCores = selectedIT.cpu;
			form.memory = selectedIT.memory;
		}
		form.mode = next;
	}
</script>

{#snippet preferenceField()}
	<!-- Rendered once per sizing branch: the field is identical in both. -->
	<FormField label="Preference">
		<SelectInput bind:value={form.preference}>
			<option value="">— unchanged —</option>
			{#each options?.preferences ?? [] as p (p.name)}
				<option value={p.name}>{p.displayName || p.name}</option>
			{/each}
		</SelectInput>
	</FormField>
{/snippet}

{#if optionsError}
	<Note tone="warn" border class="mb-3">
		Couldn't load cluster options: {optionsError} — the instance type / preference dropdowns may be empty.
	</Note>
{/if}

<!-- Sizing mode: CPU/memory OR an instance type, never both -->
<div class="mb-3">
	<span class="text-ink-muted">Sizing</span>
	<div class="mt-1 inline-flex overflow-hidden rounded border border-line-strong">
		<button
			type="button"
			onclick={() => setMode('instancetype')}
			class="px-3 py-1 text-xs {form.mode === 'instancetype'
				? 'bg-accent text-white'
				: 'bg-panel text-ink-soft hover:bg-inset'}"
		>
			Instance type
		</button>
		<button
			type="button"
			onclick={() => setMode('custom')}
			class="border-l border-line-strong px-3 py-1 text-xs {form.mode === 'custom'
				? 'bg-accent text-white'
				: 'bg-panel text-ink-soft hover:bg-inset'}"
		>
			Custom CPU/Memory
		</button>
	</div>
	{#if form.mode !== (vm.instancetype ? 'instancetype' : 'custom')}
		<p class="mt-1 text-xs text-warn-ink">
			{form.mode === 'custom'
				? 'Switches to custom CPU/memory and removes the instance type.'
				: 'Uses the instance type and removes the custom CPU/memory.'}
		</p>
	{/if}
</div>

<div class="grid grid-cols-2 gap-3">
	<FormField label="Power state">
		<SelectInput bind:value={form.power}>
			<option value="On">On</option>
			<option value="Off">Off</option>
			{#if form.power === 'Unknown'}<option value="Unknown">Unknown</option>{/if}
		</SelectInput>
	</FormField>

	{#if form.mode === 'instancetype'}
		<FormField label="Instance type">
			<SelectInput bind:value={form.instancetype}>
				<!-- Keep the current value selectable even if it isn't in the cluster
				     list (orphaned ref, or options still loading) so the binding can't
				     silently desync to the first option. -->
				{#if form.instancetype && !selectedIT}
					<option value={form.instancetype}
						>{form.instancetype} (current — not in cluster list)</option
					>
				{/if}
				{#each options?.instancetypes ?? [] as it (it.name)}
					<option value={it.name}>{it.name} ({it.cpu} CPU / {it.memory})</option>
				{/each}
			</SelectInput>
		</FormField>
		{@render preferenceField()}
		<p class="col-span-2 text-xs text-ink-faint">
			CPU and memory are provided by the instance type{selectedIT
				? `: ${selectedIT.cpu} CPU / ${selectedIT.memory}`
				: ''}.
		</p>
	{:else}
		<FormField label="vCPUs">
			<TextInput type="number" min="1" bind:value={form.cpuCores} />
		</FormField>
		<FormField label="Memory">
			<TextInput bind:value={form.memory} placeholder="2Gi" />
		</FormField>
		{@render preferenceField()}
		{#if !(form.cpuCores && form.memory)}
			<p class="col-span-2 text-xs text-warn-ink">
				Set both vCPUs and memory to apply custom sizing.
			</p>
		{:else if prefViolation}
			<p class="col-span-2 text-xs text-danger-ink">{prefViolation}</p>
		{/if}
	{/if}
</div>
