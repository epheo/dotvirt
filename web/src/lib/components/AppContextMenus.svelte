<script lang="ts">
	import { goto } from '$app/navigation';
	import { api, Unauthorized, type VM } from '$lib/api';
	import { manifestURL, type VMAction } from '$lib/actions';
	import { vmHref } from '$lib/nav';
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
	const canNamespace = $derived(!!inventory.caps?.namespace);

	async function onCtxPick(a: VMAction) {
		if (ui.ctx?.kind !== 'vm') return;
		const vm = ui.ctx.vm;
		ui.ctx = null;
		if (a.kind === 'runtime' && a.run) {
			const verb = a.verb ?? a.label;
			try {
				await a.run(vm);
				ui.recordAction({ verb, namespace: vm.namespace, name: vm.name, ok: true });
				ui.showToast(`${verb} requested for ${vm.name}.`);
			} catch (e) {
				if (e instanceof Unauthorized) return;
				ui.recordAction({ verb, namespace: vm.namespace, name: vm.name, ok: false });
				ui.showToast(String(e));
			}
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
					label: 'Review & propose',
					run: () => (ui.changesOpen = true)
				});
			} catch (e) {
				if (e instanceof Unauthorized) return;
				ui.showToast(String(e));
			}
			return;
		}
		ui.requestDetail(a.id as 'edit' | 'delete' | 'console' | 'snapshot' | 'clone' | 'template');
		goto(vmHref(vm.namespace, vm.name));
	}

	// Untracked (NotTracked) VMs in the given namespaces — the rows a bulk adopt
	// acts on. Drives both the "Adopt N untracked" label and which namespaces to call.
	function untrackedVMs(namespaces: string[]): VM[] {
		const want = new Set(namespaces);
		return inventory.allVMs.filter((v) => want.has(v.namespace) && v.sync === 'NotTracked');
	}

	// Bulk-adopt every untracked VM under a container into one draft. Only namespaces
	// that actually have untracked VMs are called (AdoptNamespace 400s on an empty one).
	async function bulkAdoptUntracked(namespaces: string[]) {
		const want = new Set(untrackedVMs(namespaces).map((v) => v.namespace));
		try {
			for (const ns of want) await api.adoptNamespace(ns);
			ui.showToast('Untracked VMs staged into Changes — open a PR to adopt them into git.', {
				label: 'Review & propose',
				run: () => (ui.changesOpen = true)
			});
		} catch (e) {
			if (e instanceof Unauthorized) return;
			ui.showToast(String(e));
		} finally {
			// Reflect whatever got staged before any failure — a mid-loop error still
			// leaves the earlier namespaces' adopts in the draft.
			await drafts.refresh();
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
					<div class="my-1 border-t border-slate-100"></div>
				{/if}
				{#if ctx.repo && untracked.length}
					<MenuItem
						onclick={() => {
							const ns = ctx.kind === 'container' ? ctx.namespaces : [];
							ui.ctx = null;
							bulkAdoptUntracked(ns);
						}}
						title="Stage every untracked VM here into one PR"
						>Adopt {untracked.length} untracked…</MenuItem
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
				<div class="my-1 border-t border-slate-100"></div>
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
