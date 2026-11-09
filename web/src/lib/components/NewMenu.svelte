<script lang="ts">
	import { page } from '$app/state';
	import { Plus } from '@lucide/svelte';
	import { ACCESS_UNKNOWN } from '#lib/authoring.js';
	import { clusterGate } from '#lib/gates.js';
	import { scopeFromPath } from '#lib/nav.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { vmsOff } from '#lib/state/modules.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from './Button.svelte';
	import { newFromHere } from './create/sheet.svelte.js';

	// New opens the create sheet on what the section in view makes most,
	// prefilled with the namespace in view; the sheet's chips reach every
	// other kind, so there is no menu to pick from first.

	// Repo-backed namespaces under the current URL's scope - what a new VM
	// pre-targets, as the tree's "New VM here" does.
	const scopeNamespaces = $derived.by(() => {
		const sc = scopeFromPath(page.url.pathname);
		if (sc.kind === 'project' || sc.kind === 'namespace') {
			const p = inventory.projects.find((proj) => proj.name === sc.project);
			if (!p?.repo) return null;
			return sc.kind === 'namespace' ? [sc.namespace] : p.namespaces.map((n) => n.namespace);
		}
		return null;
	});
	// The namespace in view: a namespace scope, a VM page, or a project's
	// first namespace. Empty leaves the sheet to its own first choice.
	const scopeNamespace = $derived.by(() => {
		const parts = page.url.pathname.split('/').slice(1).map(decodeURIComponent);
		if (parts[0] === 'vm' && parts.length >= 2) return parts[1];
		return scopeNamespaces?.[0] ?? '';
	});

	// Something to create: a namespace that takes changes, or any platform
	// change (cluster.platform is the union of the platform-tier fields).
	const platform = $derived(clusterGate.platform());
	const canCreate = $derived(inventory.namespaces.length > 0 || platform.ok);
</script>

<Button
	onclick={() =>
		(ui.modal = newFromHere(page.url.pathname + page.url.search, scopeNamespace, scopeNamespaces))}
	disabled={!canCreate}
	title={canCreate
		? vmsOff()
			? 'Create a network, rule or project'
			: 'Create a VM, network, rule or project'
		: inventory.accessUnknown || platform.why === ACCESS_UNKNOWN
			? ACCESS_UNKNOWN
			: 'No project you may change yet'}
>
	<Plus size={14} /> New
</Button>
