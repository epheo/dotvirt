<script lang="ts">
	import { untrack } from 'svelte';
	import { Copy } from 'lucide-svelte';
	import { api, Unauthorized, type Clone, type VM } from '$lib/api';
	import { relativeAge } from '$lib/format';
	import { pollWhileVisible } from '$lib/poll';
	import Modal from './Modal.svelte';

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
	let clones = $state<Clone[] | null>(null);

	// RFC 1123 label, the same constraint the API server enforces on VM names.
	const valid = $derived(
		/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(target) && target.length <= 63 && target !== vm.name,
	);

	async function load() {
		try {
			clones = await api.clones(vm.namespace, vm.name);
		} catch (e) {
			if (e instanceof Unauthorized) return;
			// Listing may fail (e.g. RBAC grants create only); keep the form usable.
			clones = clones ?? [];
		}
	}

	// Load once on mount (untracked: the host hands down a fresh vm each frame,
	// but this modal acts on the one it opened for).
	$effect(() => {
		untrack(load);
	});

	// Poll while any clone is still progressing so phases settle live (a clone
	// with no phase yet counts as in progress), paused while backgrounded.
	const active = $derived(
		clones?.some((c) => c.phase !== 'Succeeded' && c.phase !== 'Failed') ?? false,
	);
	$effect(() => {
		if (!active) return;
		return pollWhileVisible(load, 3000);
	});

	async function create() {
		busy = true;
		error = '';
		try {
			await api.createClone(vm.namespace, vm.name, target.trim());
			ondone?.(true);
			await load();
		} catch (e) {
			if (e instanceof Unauthorized) return;
			error = String(e);
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
		<label for="clone-target-input" class="mb-1 block text-xs text-ink-muted">New VM name:</label>
		<div class="flex items-center gap-2">
			<input
				id="clone-target-input"
				data-autofocus
				bind:value={target}
				class="flex-1 rounded border border-line-strong px-2 py-1.5 font-mono text-sm focus:border-accent/60"
				placeholder="{vm.name}-clone"
			/>
			<button
				onclick={create}
				disabled={!valid || busy}
				class="flex items-center gap-1.5 rounded bg-accent px-3 py-1.5 text-sm font-medium text-white hover:bg-accent disabled:bg-line-strong"
			>
				<Copy size={14} />
				{busy ? 'Cloning…' : 'Clone'}
			</button>
		</div>
		{#if target && !valid}
			<p class="mt-1 text-xs text-amber-700">
				Lowercase letters, digits and dashes only (≤63 chars), and not the source's own name.
			</p>
		{/if}
		{#if error}
			<pre class="mt-2 rounded bg-red-50 p-2 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
		{/if}

		{#if clones && clones.length}
			<h3 class="mt-4 mb-1 text-xs font-semibold tracking-wide text-ink-muted uppercase">
				Clones of this VM
			</h3>
			<table class="w-full text-[13px]">
				<thead class="text-left text-xs tracking-wide text-ink-faint uppercase">
					<tr class="border-b border-line">
						<th class="py-1.5 pr-3 font-medium">Target VM</th>
						<th class="py-1.5 pr-3 font-medium">Started</th>
						<th class="py-1.5 font-medium">Status</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-line-soft">
					{#each clones as c (c.name)}
						<tr>
							<td class="py-1.5 pr-3 font-medium text-ink">{c.target}</td>
							<td class="py-1.5 pr-3 whitespace-nowrap text-ink-muted">{relativeAge(c.created)}</td>
							<td class="py-1.5 whitespace-nowrap">
								{#if c.phase === 'Succeeded'}
									<span class="inline-flex items-center gap-1.5 text-green-700">
										<span class="h-1.5 w-1.5 rounded-full bg-green-500"></span> Succeeded
									</span>
								{:else if c.phase === 'Failed'}
									<span class="inline-flex items-center gap-1.5 text-red-700">
										<span class="h-1.5 w-1.5 rounded-full bg-red-500"></span> Failed
									</span>
								{:else}
									<span class="inline-flex items-center gap-1.5 text-amber-600">
										<span class="h-1.5 w-1.5 animate-pulse rounded-full bg-amber-500"></span>
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
			class="ml-auto rounded border border-line-strong px-3 py-1 text-sm text-ink-soft hover:bg-inset"
		>
			Close
		</button>
	{/snippet}
</Modal>
