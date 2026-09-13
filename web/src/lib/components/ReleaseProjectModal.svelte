<script lang="ts">
	import { api, type ReleaseResult } from '$lib/api';
	import { action } from '$lib/resource.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import ConfirmDelete from './ConfirmDelete.svelte';

	// Release a repoless project: the "no repo configured" dead-end's way BACK.
	// Declared tenancy stages a platform-repo rewrite (a PR is the release);
	// label residue is stripped imperatively under the caller's token and the
	// namespace returns to Existing tenants (or disappears if it runs nothing).
	let {
		project,
		namespaces,
		onclose,
	}: { project: string; namespaces: string[]; onclose: () => void } = $props();

	const op = action();

	function release() {
		// Snapshot the props: onclose() unmounts this modal and its prop
		// expressions (m.project) die with it - reading them after is a TypeError.
		const name = project;
		return op.run(async () => {
			const r: ReleaseResult = await api.releaseProject(name);
			onclose();
			if (r.staged?.length) {
				ui.toastStaged(
					`Release of ${name} staged for ${r.staged.join(', ')} — merges apply it` +
						(r.released?.length ? `; ${r.released.join(', ')} released immediately.` : '.'),
				);
			} else {
				ui.showToast(
					`${name} released — ${r.released?.join(', ') || 'its namespaces'} no longer carry the project label.`,
					{ kind: 'success' },
				);
			}
		});
	}
</script>

<ConfirmDelete
	title="Release project"
	confirmWord={project}
	verb="Release"
	busy={op.busy}
	error={op.error}
	onconfirm={release}
	{onclose}
>
	<p>
		<strong>{project}</strong> has the project label but no backing repo. Releasing removes
		dotvirt's claim on {namespaces.length === 1
			? 'its namespace'
			: `its ${namespaces.length} namespaces`}
		({namespaces.join(', ')}):
	</p>
	<ul class="mt-2 list-disc space-y-1 pl-5 text-xs">
		<li>
			Namespaces declared in the platform repo are rewritten without the label — a pull request
			applies it.
		</li>
		<li>
			Label-only residue is unlabelled immediately under your own permissions — no VM, disk or
			network is touched.
		</li>
	</ul>
	<p class="mt-2 text-xs">
		Namespaces that still run VMs reappear under Existing tenants, adoptable again.
	</p>
</ConfirmDelete>
