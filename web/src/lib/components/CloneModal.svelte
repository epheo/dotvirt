<script lang="ts">
	import { Copy } from 'lucide-svelte';
	import { api, Unauthorized, type Clone, type VM } from '$lib/api';
	import { friendlyError, relativeAge } from '$lib/format';
	import { resource, type Resource } from '$lib/resource.svelte';
	import { TBODY, TH, TH_LAST, THEAD, THEAD_TR } from '$lib/table';
	import { validName } from '$lib/validate';
	import ErrorNote from './ErrorNote.svelte';
	import FormField from './FormField.svelte';
	import Modal from './Modal.svelte';
	import StatusDot from './StatusDot.svelte';
	import TextInput from './TextInput.svelte';

	// Clone name-prompt + progress: creating a VirtualMachineClone is imperative
	// (RBAC-gated, like snapshots), but the resulting target VM is config state
	// that exists only in the cluster — it appears in the inventory as "Not in
	// git" until adopted, which the hint below points at.
	let {
		vm,
		onclose,
		ondone,
	}: {
		vm: VM;
		onclose: () => void;
		// Reports the create request's outcome (for the Recent Tasks dock).
		ondone?: (ok: boolean) => void;
	} = $props();

	// The prefill seeds from the VM the modal opened for; the host closes the
	// modal on selection change, so the initial capture is the intent.
	// svelte-ignore state_referenced_locally
	let target = $state(vm.name + '-clone');
	let busy = $state(false);
	let error = $state('');

	// RFC 1123 label, the same constraint the API server enforces on VM names.
	const valid = $derived(validName(target) && target !== vm.name);

	// Constant key: the modal acts on the VM it opened for. Polls only while a
	// clone is still progressing (no phase yet counts as in progress). Listing
	// may fail (e.g. RBAC grants create only); failed maps to [] to keep the
	// form usable. Explicit binding type: the poll gate reads back through it.
	const clonesRes: Resource<Clone[]> = resource(
		() => '',
		() => api.clones(vm.namespace, vm.name),
		{ poll: () => (active ? 3000 : 0) },
	);
	const clones = $derived(clonesRes.data ?? (clonesRes.failed ? [] : null));
	const active = $derived(
		clones?.some((c) => c.phase !== 'Succeeded' && c.phase !== 'Failed') ?? false,
	);

	async function create() {
		busy = true;
		error = '';
		try {
			await api.createClone(vm.namespace, vm.name, target.trim());
			ondone?.(true);
			await clonesRes.refresh();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = friendlyError(e);
			ondone?.(false);
		} finally {
			busy = false;
		}
	}
</script>

<Modal title="Clone — {vm.name}" size="lg" {onclose}>
	<div class="min-h-0 flex-1 overflow-y-auto px-5 py-4 text-sm text-ink-soft">
		<p class="mb-3 text-xs text-ink-muted">
			Clones via snapshot + restore (the source may stay running). The new VM exists only in the
			cluster at first — open it and use <strong>Adopt into git</strong> to propose its manifest.
		</p>
		<FormField
			label="New VM name:"
			error={target && !valid
				? "Lowercase letters, digits and dashes only (≤63 chars), and not the source's own name."
				: ''}
		>
			<div class="flex items-center gap-2">
				<TextInput
					data-autofocus
					bind:value={target}
					mono
					class="flex-1"
					placeholder="{vm.name}-clone"
				/>
				<button
					onclick={create}
					disabled={!valid || busy}
					class="flex shrink-0 items-center gap-1.5 rounded bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent-hover disabled:bg-line-strong"
				>
					<Copy size={14} />
					{busy ? 'Cloning…' : 'Clone'}
				</button>
			</div>
		</FormField>
		<ErrorNote {error} class="mt-2" />

		{#if clones && clones.length}
			<h3 class="mt-4 mb-1 text-xs font-semibold tracking-wide text-ink-muted uppercase">
				Clones of this VM
			</h3>
			<table class="w-full text-[13px]">
				<thead class={THEAD}>
					<tr class={THEAD_TR}>
						<th class={TH}>Target VM</th>
						<th class={TH}>Started</th>
						<th class={TH_LAST}>Status</th>
					</tr>
				</thead>
				<tbody class={TBODY}>
					{#each clones as c (c.name)}
						<tr>
							<td class="py-1.5 pr-3 font-medium text-ink">{c.target}</td>
							<td class="py-1.5 pr-3 whitespace-nowrap text-ink-muted">{relativeAge(c.created)}</td>
							<td class="py-1.5 whitespace-nowrap">
								{#if c.phase === 'Succeeded'}
									<span class="inline-flex items-center gap-1.5 text-ok-ink">
										<StatusDot tone="ok" size="xs" /> Succeeded
									</span>
								{:else if c.phase === 'Failed'}
									<span class="inline-flex items-center gap-1.5 text-danger-ink">
										<StatusDot tone="danger" size="xs" /> Failed
									</span>
								{:else}
									<span class="inline-flex items-center gap-1.5 text-warn-ink">
										<StatusDot tone="warn" size="xs" pulse />
										{c.phase || 'Starting…'}
									</span>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
	</div>
	{#snippet footer()}
		<button
			onclick={onclose}
			class="ml-auto rounded px-4 py-1.5 text-sm text-ink-soft hover:bg-inset-strong"
		>
			Close
		</button>
	{/snippet}
</Modal>
