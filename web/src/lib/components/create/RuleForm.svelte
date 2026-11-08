<script lang="ts">
	import type {
		AdminNetworkPolicyCreate,
		EgressFirewallCreate,
		NetworkPolicyCreate,
	} from '#lib/api.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import ClusterRuleForm from './ClusterRuleForm.svelte';
	import OutboundRuleForm from './OutboundRuleForm.svelte';
	import ProjectRuleForm from './ProjectRuleForm.svelte';
	import { LABEL, type CreateModal } from './sheet.svelte.js';

	// A firewall rule, by what it controls: traffic between VMs of a project,
	// cluster rules over every project, or where a project's VMs may go. The
	// opener's kind picks the start; an edit stays its own kind.
	let {
		namespace = $bindable(),
		modal,
		onclose,
	}: { namespace: string; modal: CreateModal | null; onclose: () => void } = $props();

	type Kind = 'project' | 'cluster' | 'outbound';
	// svelte-ignore state_referenced_locally
	let kind = $state<Kind>(
		modal?.kind === 'adminFw' ? 'cluster' : modal?.kind === 'egressFw' ? 'outbound' : 'project',
	);
	// svelte-ignore state_referenced_locally
	const initial = modal && 'initial' in modal ? modal.initial : undefined;
	const canProject = $derived(inventory.namespaces.length > 0);
	const KINDS: { id: Kind; label: string; text: string }[] = [
		{ id: 'project', label: 'Project rule', text: 'Which VMs may reach VMs in one project.' },
		{
			id: 'cluster',
			label: 'Cluster rule',
			text: 'Across every project, in priority order. Projects cannot override it.',
		},
		{ id: 'outbound', label: 'Outbound rule', text: "Where a project's VMs may go outside." },
	];
	const kinds = $derived(
		KINDS.filter((k) => k.id === kind || (k.id === 'cluster' ? inventory.canAdminFw : canProject)),
	);
</script>

{#snippet chooser()}
	{#if !initial}
		<fieldset class="min-w-0">
			<legend class="mb-2 {LABEL}">What it controls</legend>
			<div class="flex flex-col overflow-hidden rounded-lg border border-line">
				{#each kinds as k (k.id)}
					{@const on = kind === k.id}
					<label
						class="grid cursor-pointer grid-cols-[16px_minmax(0,1fr)] items-start gap-x-3 border-t border-line-soft px-3.5 py-3 first:border-t-0 {on
							? 'bg-select-soft shadow-[inset_3px_0_0_var(--color-accent)]'
							: 'hover:bg-inset'}"
					>
						<input
							type="radio"
							name="rule-kind"
							value={k.id}
							bind:group={kind}
							class="mt-0.5 size-4 accent-accent"
						/>
						<span class="flex min-w-0 flex-wrap items-baseline gap-x-2.5">
							<b class="font-bold text-ink">{k.label}</b>
							<span class="text-[13px] text-ink-muted">{k.text}</span>
						</span>
					</label>
				{/each}
			</div>
		</fieldset>
	{/if}
{/snippet}

{#key kind}
	{#if kind === 'project'}
		<ProjectRuleForm
			bind:namespace
			initial={modal?.kind === 'dfw' ? (initial as NetworkPolicyCreate | undefined) : undefined}
			{chooser}
			{onclose}
		/>
	{:else if kind === 'cluster'}
		<ClusterRuleForm
			initial={modal?.kind === 'adminFw'
				? (initial as AdminNetworkPolicyCreate | undefined)
				: undefined}
			{chooser}
			{onclose}
		/>
	{:else}
		<OutboundRuleForm
			bind:namespace
			initial={modal?.kind === 'egressFw'
				? (initial as EgressFirewallCreate | undefined)
				: undefined}
			{chooser}
			{onclose}
		/>
	{/if}
{/key}
