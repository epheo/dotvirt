<script lang="ts">
	import type { NodeTarget, VM } from '$lib/api';
	import type { EditForm } from '$lib/editform';
	import { splitList } from '$lib/format';
	import type { Resource } from '$lib/resource.svelte';
	import { validName, NAME_HINT } from '$lib/validate';
	import Button from '../Button.svelte';
	import CheckGroup from '../CheckGroup.svelte';
	import FormField from '../FormField.svelte';
	import Note from '../Note.svelte';
	import SelectInput from '../SelectInput.svelte';
	import TextInput from '../TextInput.svelte';

	let {
		form = $bindable(),
		pinText = $bindable(),
		vm,
		hosts,
	}: {
		form: EditForm;
		// Owned by the dialog so the free-text pin list survives step changes as typed.
		pinText: string;
		vm: VM;
		hosts: Resource<NodeTarget[]>;
	} = $props();

	// Offer every schedulable-ish host, plus any already-pinned name that no
	// longer exists (so a stale pin can still be unchecked).
	const hostItems = $derived.by(() => {
		const nodes = hosts.data ?? [];
		const names = new Set(nodes.map((n) => n.name));
		for (const h of form.pin) names.add(h);
		return [...names].sort().map((n) => {
			const node = nodes.find((x) => x.name === n);
			return {
				value: n,
				hint: !node
					? 'unknown'
					: node.unschedulable
						? 'cordoned'
						: node.maintenance
							? 'maintenance'
							: '',
			};
		});
	});
	function syncPinText() {
		form.pin = splitList(pinText);
	}

	const customScheduling = $derived(!!vm.scheduling?.custom);

	function addGroup() {
		form.groups = [
			...form.groups,
			{ name: '', mode: 'together', strict: true, removed: false, isNew: true },
		];
	}
</script>

<div class="space-y-4">
	<!-- Placement groups: keep-together / keep-apart rules. Membership + rule are one
	     encoding on the manifest, so this edits both at once. -->
	<div>
		<div class="mb-1 flex items-center justify-between">
			<span class="text-ink-muted">Placement groups</span>
			{#if !customScheduling}
				<Button variant="link" size="sm" onclick={addGroup}>+ Add group</Button>
			{/if}
		</div>
		{#if customScheduling}
			<Note tone="warn" border>
				This VM carries hand-written affinity or node selection — placement is managed in git, not
				from this form.
			</Note>
		{:else}
			{#each form.groups as group, i (i)}
				<div
					class="mb-1 flex flex-wrap items-center gap-2 {group.removed
						? 'opacity-40 line-through'
						: ''}"
				>
					{#if group.isNew}
						<TextInput bind:value={group.name} placeholder="web-tier" mono class="w-40 flex-none" />
					{:else}
						<span class="w-40 truncate font-mono text-[13px] text-ink">{group.name}</span>
					{/if}
					<SelectInput bind:value={group.mode} class="w-56 flex-none" disabled={group.removed}>
						<option value="together">Keep together — same host</option>
						<option value="apart">Keep apart — different hosts</option>
					</SelectInput>
					<label class="flex items-center gap-1.5 text-xs text-ink-soft">
						<input type="checkbox" bind:checked={group.strict} disabled={group.removed} />
						strict
					</label>
					<button
						onclick={() =>
							group.isNew
								? (form.groups = form.groups.filter((_, idx) => idx !== i))
								: (group.removed = !group.removed)}
						class="ml-auto text-xs {group.removed ? 'text-accent' : 'text-danger'}"
					>
						{group.removed ? 'undo' : 'remove'}
					</button>
					{#if group.isNew && group.name && !validName(group.name)}
						<p class="w-full text-xs text-warn-ink">{NAME_HINT}</p>
					{/if}
				</div>
			{:else}
				<p class="text-xs text-ink-faint">
					No groups. VMs sharing a group are kept on one host (together) or spread across hosts
					(apart) — strict rules bind the scheduler, non-strict are best effort.
				</p>
			{/each}
		{/if}
	</div>

	<!-- Host pinning: a required node-affinity host list. -->
	{#if !customScheduling}
		<div>
			<span class="mb-1 block text-ink-muted">Pin to hosts</span>
			{#if hosts.loading}
				<p class="text-xs text-ink-faint">Loading hosts…</p>
			{:else if !hosts.failed && hostItems.length}
				<CheckGroup items={hostItems} bind:selected={form.pin} />
			{:else}
				<TextInput
					bind:value={pinText}
					oninput={syncPinText}
					placeholder="w1 w2 (space separated; empty = any host)"
					mono
				/>
			{/if}
			<p class="mt-1 text-xs text-ink-faint">
				{form.pin.length
					? 'The scheduler may only place this VM on the checked hosts.'
					: 'No pinning — the scheduler places this VM on any eligible host.'}
			</p>
		</div>
	{/if}

	<!-- Rescheduling participation + eviction behavior (the per-VM automation
	     override). -->
	<div class="border-t border-line-soft pt-3">
		<label class="flex items-start gap-2 text-[13px]">
			<input type="checkbox" bind:checked={form.drsExclude} class="mt-0.5" />
			<span>
				Exclude from load balancing
				<span class="block text-xs text-ink-faint">
					Automatic rebalancing skips this VM; node drains still live-migrate it.
				</span>
			</span>
		</label>
		<FormField label="Eviction strategy">
			<SelectInput bind:value={form.evictionStrategy}>
				<option value="">Cluster default</option>
				<option value="LiveMigrate">LiveMigrate — evictions live-migrate the VM</option>
				<option value="LiveMigrateIfPossible"
					>LiveMigrateIfPossible — migrate when possible, else restart</option
				>
				<option value="None">None — pinned (blocks node drains)</option>
			</SelectInput>
		</FormField>
	</div>
</div>
