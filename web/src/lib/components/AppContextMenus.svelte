<script lang="ts">
	import { goto } from '$app/navigation';
	import type { VM } from '$lib/api';
	import { adoptNamespaces, dispatchVMAction, type VMAction } from '$lib/actions';
	import { repoError } from '$lib/gitops';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import ActionMenu from './ActionMenu.svelte';
	import ContextMenu from './ContextMenu.svelte';
	import MenuItem from './MenuItem.svelte';

	// The shell-level right-click menus: a VM row (the action registry) and a
	// project/namespace row (container verbs). The bulk variant renders inside
	// the workspace that owns the grid selection.
	const ctx = $derived(ui.ctx);

	async function onCtxPick(a: VMAction) {
		if (ui.ctx?.kind !== 'vm') return;
		const vm = ui.ctx.vm;
		ui.ctx = null;
		await dispatchVMAction(a, vm, { onstaged: () => drafts.refresh() });
	}

	// Recover-repo gate: only a comparison-plane error says the forge lost the
	// repo. An apply failure (operation Failed/Error) is a manifest problem the
	// recovery flow must not offer on.
	function projectSyncError(project: string): string {
		return repoError(inventory.inventory?.projects.find((p) => p.name === project)?.gitOps);
	}

	// Untracked VMs are what the inventory can see git not describing, so they decide
	// whether adoption is offered and which namespaces to call. The adopt itself takes
	// the whole namespace, VMs included, so a namespace never lands half declared.
	function untrackedVMs(namespaces: string[]): VM[] {
		const want = new Set(namespaces);
		return inventory.allVMs.filter((v) => want.has(v.namespace) && v.sync === 'NotTracked');
	}

	// Adopt each namespace whole into one draft. No untracked VMs (recovery: all
	// tracked, none declared) falls back to every namespace; per-ns 400s surface
	// rather than silently skipping.
	async function bulkAdoptUntracked(namespaces: string[]) {
		let want = new Set(untrackedVMs(namespaces).map((v) => v.namespace));
		if (want.size === 0) want = new Set(namespaces);
		await adoptNamespaces(want, { onstaged: () => drafts.refresh() });
	}
</script>

{#if ctx}
	<ContextMenu x={ctx.x} y={ctx.y} onclose={() => (ui.ctx = null)}>
		{#if ctx.kind === 'vm'}
			<ActionMenu vm={ctx.vm} onpick={onCtxPick} />
		{:else}
			{@const untracked = untrackedVMs(ctx.namespaces)}
			<div class="w-48 rounded border border-line bg-panel py-1 text-xs shadow-lg">
				<div class="truncate px-3 py-1 text-[10px] tracking-wide text-ink-faint uppercase">
					{ctx.namespace ?? ctx.project}
				</div>
				{#if !ctx.repo && inventory.canNamespace}
					<MenuItem
						onclick={() => {
							ui.modal =
								ctx.kind === 'container'
									? { kind: 'adoptProject', project: ctx.project, namespaces: ctx.namespaces }
									: null;
							ui.ctx = null;
						}}
						title="Create a repo for this project and bring it under GitOps">Attach repo…</MenuItem
					>
					<MenuItem
						onclick={() => {
							ui.modal =
								ctx.kind === 'container'
									? { kind: 'releaseProject', project: ctx.project, namespaces: ctx.namespaces }
									: null;
							ui.ctx = null;
						}}
						title="Dissolve this project: remove the project label so its namespaces go back to plain (or adoptable) namespaces"
						>Release project…</MenuItem
					>
					<div class="my-1 border-t border-line-soft"></div>
				{/if}
				<!-- A dead repo annotation is a different dead end from repoless. Gated on
				     the GitOps error; the backend refuses when the repo resolves. -->
				{#if ctx.kind === 'container' && ctx.repo && projectSyncError(ctx.project) && inventory.canNamespace}
					<MenuItem
						onclick={() => {
							ui.modal = {
								kind: 'adoptProject',
								project: ctx.project,
								namespaces: ctx.namespaces,
								recover: true,
							};
							ui.ctx = null;
						}}
						title="Re-create this project's lost repo and re-adopt what is running"
						>Recover repo…</MenuItem
					>
					<div class="my-1 border-t border-line-soft"></div>
				{/if}
				{#if ctx.repo && (untracked.length || projectSyncError(ctx.project))}
					<MenuItem
						onclick={() => {
							const ns = ctx.kind === 'container' ? ctx.namespaces : [];
							ui.ctx = null;
							bulkAdoptUntracked(ns);
						}}
						title="Capture the live manifests of everything running here that git does not describe (VMs, networks, policies) into one PR"
						>Adopt into git</MenuItem
					>
				{/if}
				<MenuItem
					onclick={() => {
						const ns = ctx.kind === 'container' ? ctx.namespaces : null;
						ui.ctx = null;
						ui.modal = { kind: 'newVM', namespaces: ns };
					}}
					disabled={!ctx.repo}
					title={ctx.repo ? '' : 'Project has no backing repo'}>New VM here…</MenuItem
				>
				<MenuItem
					onclick={() => {
						const c = ctx.kind === 'container' ? ctx : null;
						ui.ctx = null;
						ui.modal = c
							? { kind: 'egressFw', namespaces: c.namespaces, namespace: c.namespace }
							: null;
					}}
					disabled={!ctx.repo}
					title={ctx.repo
						? 'Add a north-south egress firewall (the Tier-1 gateway firewall)'
						: 'Project has no backing repo'}>New Egress Firewall…</MenuItem
				>
				<MenuItem
					onclick={() => {
						const c = ctx.kind === 'container' ? ctx : null;
						ui.ctx = null;
						ui.modal = c ? { kind: 'dfw', namespaces: c.namespaces, namespace: c.namespace } : null;
					}}
					disabled={!ctx.repo}
					title={ctx.repo
						? 'Add an east-west Distributed Firewall policy (NetworkPolicy)'
						: 'Project has no backing repo'}>New Security Policy…</MenuItem
				>
				{#if inventory.canNamespace}
					<MenuItem
						onclick={() => {
							const project = ctx.kind === 'container' ? ctx.project : null;
							ui.ctx = null;
							ui.modal = { kind: 'namespace', project };
						}}
						disabled={!ctx.repo}
						title={ctx.repo ? '' : 'Project has no backing repo'}>New Namespace here…</MenuItem
					>
				{/if}
				<div class="my-1 border-t border-line-soft"></div>
				<MenuItem
					onclick={() => {
						const project = ctx.kind === 'container' ? ctx.project : null;
						ui.ctx = null;
						goto(`/networking/security${project ? `?tenant=${encodeURIComponent(project)}` : ''}`);
					}}
					title="The policy plane scoped to this tenant">Security view</MenuItem
				>
				<MenuItem
					onclick={() => {
						if (ctx.kind === 'container' && ctx.repo) window.open(ctx.repo, '_blank');
						ui.ctx = null;
					}}
					disabled={!ctx.repo}>Open repository ↗</MenuItem
				>
				<MenuItem
					onclick={() => {
						ui.ctx = null;
						ui.openChanges();
					}}>Changes &amp; history</MenuItem
				>
			</div>
		{/if}
	</ContextMenu>
{/if}
