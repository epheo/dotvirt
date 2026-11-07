<script lang="ts">
	import { api, vmKey } from '#lib/api.js';
	import { action } from '#lib/resource.svelte.js';
	import { drafts } from '#lib/state/drafts.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Button from './Button.svelte';
	import CreateSheet from './create/CreateSheet.svelte';
	import { isCreate } from './create/sheet.svelte.js';
	import Modal from './Modal.svelte';
	import AddUplinkModal from './AddUplinkModal.svelte';
	import AdoptProjectModal from './AdoptProjectModal.svelte';
	import CloneModal from './CloneModal.svelte';
	import ReleaseProjectModal from './ReleaseProjectModal.svelte';
	import DeleteObjectModal from './DeleteObjectModal.svelte';
	import DeleteVMModal from './DeleteVMModal.svelte';
	import EditManifestModal from './EditManifestModal.svelte';
	import EditSettings from './EditSettings.svelte';
	import MigrateModal from './MigrateModal.svelte';
	import NewNamespaceModal from './NewNamespaceModal.svelte';
	import ProposeSheet from './ProposeSheet.svelte';
	import SaveTemplateModal from './SaveTemplateModal.svelte';
	import StagedChangesModal from './StagedChangesModal.svelte';
	import StorageMigrateModal from './StorageMigrateModal.svelte';
	import TraceModal from '#lib/networking/TraceModal.svelte';
	import UndoModal from '#lib/workspace/UndoModal.svelte';
	import { reviewCache } from '#lib/state/reviewCache.svelte.js';

	// One host for every shell-level modal: ui.modal is a discriminated union, so
	// exactly one can be open and opening any is a single assignment.
	const m = $derived(ui.modal);
	const close = () => (ui.modal = null);

	// The per-VM staged-changes modal (opened from a Staged badge).
	const discardOp = action({ toast: ui.toastError }); // a failure leaves the modal open to retry
	const stagedItem = $derived(
		m?.kind === 'staged' ? (drafts.stagedByKey.get(vmKey(m.vm)) ?? null) : null,
	);
	function discardStaged() {
		if (m?.kind !== 'staged') return;
		const { namespace, name } = m.vm;
		return discardOp.run(async () => {
			await api.unstage(namespace, name);
			close();
			await drafts.refresh();
		});
	}
	function reviewStaged() {
		close();
		ui.openChanges();
	}

	// The VM dialogs act on the live VM, never on the frame they opened from; a
	// VM deleted under an open dialog leaves a "gone" notice, not a stale form.
	const ref = $derived(m && 'vm' in m && m.kind !== 'staged' ? m.vm : null);
	const vm = $derived(ref ? inventory.findVM(ref.namespace, ref.name) : null);
</script>

{#if isCreate(m)}
	<!-- Keyed on the request: a sheet that opens another (New VM from an
	     uploaded image) starts fresh from the new one. -->
	{#key m}<CreateSheet modal={m} onclose={close} />{/key}
{:else if m?.kind === 'trace'}
	<TraceModal source={m.source} onclose={close} />
{:else if m?.kind === 'uplink'}
	<AddUplinkModal adapters={inventory.physicalAdapters} initial={m.initial} onclose={close} />
{:else if m?.kind === 'namespace'}
	<NewNamespaceModal
		projects={inventory.repoProjects}
		project={m.project ?? undefined}
		onclose={close}
	/>
{:else if m?.kind === 'adoptProject'}
	<AdoptProjectModal
		project={m.project}
		namespaces={m.namespaces}
		recover={m.recover}
		onclose={close}
	/>
{:else if m?.kind === 'releaseProject'}
	<ReleaseProjectModal project={m.project} namespaces={m.namespaces} onclose={close} />
{:else if m?.kind === 'deleteObject'}
	<DeleteObjectModal
		resource={m.resource}
		namespace={m.namespace}
		name={m.name}
		sourceFile={m.sourceFile}
		onclose={close}
	/>
{:else if m?.kind === 'editTemplate'}
	{@const t = m.template}
	<EditManifestModal
		kind="template"
		title="Edit Template — {t.name}"
		sourceFile={t.sourceFile}
		yaml={t.yaml}
		summary={`Replaces ${t.sourceFile} in the ${t.library === 'platform' ? 'shared library' : t.library}`}
		onsubmit={(yaml) => api.updateTemplate({ library: t.library, name: t.name, yaml })}
		onclose={close}
	/>
{:else if m?.kind === 'editManifest'}
	{@const o = m}
	<EditManifestModal
		title="Edit manifest — {o.name}"
		sourceFile={o.sourceFile}
		yaml={o.yaml}
		reason={o.reason}
		summary={`Replaces ${o.sourceFile}`}
		onsubmit={(yaml) => api.updateObjectManifest(o.resource, o.namespace, o.name, yaml)}
		onclose={close}
	/>
{:else if m?.kind === 'staged' && stagedItem}
	<StagedChangesModal
		item={stagedItem}
		busy={discardOp.busy}
		onclose={close}
		ondiscard={discardStaged}
		onreview={reviewStaged}
	/>
{:else if ref && !vm}
	<Modal title="VM not found" onclose={close}>
		<p class="px-5 py-4 text-sm text-ink-soft">
			{ref.namespace}/{ref.name} no longer exists in the cluster.
		</p>
		{#snippet footer()}<Button class="ml-auto" onclick={close}>Close</Button>{/snippet}
	</Modal>
{:else if m?.kind === 'editVM' && vm}
	<EditSettings {vm} networks={inventory.networks} initialSection={m.section} onclose={close} />
{:else if m?.kind === 'deleteVM' && vm}
	<DeleteVMModal {vm} onclose={close} />
{:else if m?.kind === 'cloneVM' && vm}
	<CloneModal {vm} onclose={close} />
{:else if m?.kind === 'saveTemplate' && vm}
	<SaveTemplateModal {vm} onclose={close} />
{:else if m?.kind === 'migrateVM' && vm}
	<MigrateModal
		{vm}
		onclose={close}
		ondone={(ok) => {
			if (ok) ui.showToast(`Live-migration requested for ${vm.name}.`, { kind: 'success' });
		}}
	/>
{:else if m?.kind === 'migrateStorage' && vm}
	<StorageMigrateModal {vm} onclose={close} />
{:else if m?.kind === 'propose'}
	<ProposeSheet project={m.project} onclose={close} />
{:else if m?.kind === 'undo'}
	{@const u = m}
	{@const review = reviewCache.commit(u.project, u.hash)?.data}
	<UndoModal
		project={u.project}
		hash={u.hash}
		commit={review?.commit}
		warning={review?.revertWarning}
		onclose={close}
		onreverted={(r) => ui.toastProposed(u.project, r, 'Undo PR')}
	/>
{/if}
