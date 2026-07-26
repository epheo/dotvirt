<script lang="ts">
	import { goto } from '$app/navigation';
	import { api, Unauthorized, type VM } from '$lib/api';
	import { manifestURL, runRuntimeAction, type VMAction } from '$lib/actions';
	import { friendlyError } from '$lib/format';
	import { vmHref } from '$lib/nav';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui, type DetailAction } from '$lib/state/ui.svelte';
	import ActionMenu from './ActionMenu.svelte';
	import ContextMenu from './ContextMenu.svelte';
	import MenuItem from './MenuItem.svelte';

	// The shell-level right-click menus: a VM row (the action registry) and a
	// project/namespace row (container verbs). The bulk variant renders inside
	// the workspace that owns the grid selection.
	const ctx = $derived(ui.ctx);
	const canNamespace = $derived(!!inventory.caps?.namespace);

	async function onCtxPick(a: VMAction) {
		if (ui.ctx?.kind !== 'vm') return;
		const vm = ui.ctx.vm;
		ui.ctx = null;
		if (a.kind === 'runtime' && a.run) {
			await runRuntimeAction(a, vm);
			return;
		}
		if (a.id === 'manifest') {
			window.open(manifestURL(vm), '_blank');
			return;
		}
		if (a.id === 'adopt') {
			try {
				await api.adopt(vm.namespace, vm.name);
				await drafts.refresh();
				ui.showToast(`${vm.name} staged into Changes — open a PR to adopt it into git.`, {
					kind: 'success',
					action: { label: 'Review & propose', run: () => (ui.changesOpen = true) },
				});
			} catch (e) {
				if (e instanceof Unauthorized) return;
				ui.showToast(friendlyError(e), { kind: 'error' });
			}
			return;
		}
		ui.requestDetail(a.id as DetailAction);
		goto(vmHref(vm.namespace, vm.name));
	}

	// The project's GitOps rollup error, for the recover-repo gate: a repo the forge
	// lost surfaces here as a comparison error (nothing else in the payload says it).
	function projectSyncError(project: string): string {
		return inventory.inventory?.projects.find((p) => p.name === project)?.gitOps?.syncError ?? '';
	}

	// Untracked VMs are what the inventory can see git not describing, so they decide
	// whether adoption is offered and which namespaces to call. The adopt itself takes
	// the whole namespace, VMs included, so a namespace never lands half declared.
	function untrackedVMs(namespaces: string[]): VM[] {
		const want = new Set(namespaces);
		return inventory.allVMs.filter((v) => want.has(v.namespace) && v.sync === 'NotTracked');
	}

	// Adopt each namespace whole into one draft. Namespaces showing untracked VMs are
	// the usual targets; with none (a recovered repo whose objects are all tracked but
	// no longer declared), every namespace is asked and "nothing to adopt" 400s are
	// surfaced per namespace rather than silently skipping the lot.
	async function bulkAdoptUntracked(namespaces: string[]) {
		let want = new Set(untrackedVMs(namespaces).map((v) => v.namespace));
		if (want.size === 0) want = new Set(namespaces);
		// Each namespace is adopted on its own: one that has nothing left to adopt (or that
		// the caller may not read) must not abandon the rest half-done, with the already
		// staged ones sitting in the draft unmentioned.
		const caveats: string[] = [];
		// The view warning is project-wide and re-derived per call, so each response
		// restates (a shrinking version of) the same text; keep only the last, freshest
		// one instead of accumulating near-duplicates.
		let warning = '';
		let staged = 0;
		try {
			for (const ns of want) {
				try {
					const view = await api.adoptNamespace(ns);
					staged++;
					// A capture bounded by the caller's RBAC (or by what ArgoCD still tracks) is
					// worth staging, but saying so matters: silence would read as "the namespace
					// is now fully described".
					warning = view.warning ?? '';
				} catch (e) {
					if (e instanceof Unauthorized) throw e;
					caveats.push(`${ns}: ${friendlyError(e)}`);
				}
			}
		} catch (e) {
			if (e instanceof Unauthorized) return;
		} finally {
			await drafts.refresh();
		}
		const notes = [...caveats, warning].filter(Boolean).join(' ');
		if (!staged) {
			ui.showToast(notes || 'Nothing to adopt.', { kind: 'error' });
			return;
		}
		const action = { label: 'Review & propose', run: () => (ui.changesOpen = true) };
		if (notes) {
			ui.showToast(`Staged, but not everything: ${notes}`, { kind: 'warning', action });
		} else {
			ui.showToast('Staged into Changes - open a PR to adopt them into git.', {
				kind: 'success',
				action,
			});
		}
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
				{#if !ctx.repo && inventory.canManage}
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
					<div class="my-1 border-t border-line-soft"></div>
				{/if}
				<!-- A dead repo annotation (the forge lost the repo) is a different dead end from
				     repoless: recovery re-creates the repo. Gated on the GitOps error, not its text;
				     the backend refuses when the repo actually resolves, so a transient sync error
				     ends in a clear conflict rather than a wrong re-create. -->
				{#if ctx.kind === 'container' && ctx.repo && projectSyncError(ctx.project) && inventory.canManage}
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
						title="Stage everything here that git does not describe, into one PR"
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
				{#if canNamespace}
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
						ui.changesOpen = true;
					}}>Changes &amp; history</MenuItem
				>
			</div>
		{/if}
	</ContextMenu>
{/if}
