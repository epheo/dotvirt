<script lang="ts">
	import { api } from '$lib/api';
	import { drafts } from '$lib/state/drafts.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import AddUplinkModal from './AddUplinkModal.svelte';
	import AdminFirewallModal from './AdminFirewallModal.svelte';
	import AdoptProjectModal from './AdoptProjectModal.svelte';
	import DeployTemplateModal from './DeployTemplateModal.svelte';
	import DistributedFirewallModal from './DistributedFirewallModal.svelte';
	import EditTemplateModal from './EditTemplateModal.svelte';
	import EgressFirewallModal from './EgressFirewallModal.svelte';
	import NewNamespaceModal from './NewNamespaceModal.svelte';
	import NewNetworkModal from './NewNetworkModal.svelte';
	import NewProjectModal from './NewProjectModal.svelte';
	import NewVMWizard from './NewVMWizard.svelte';
	import StagedChangesModal from './StagedChangesModal.svelte';
	import Tier0Modal from './Tier0Modal.svelte';
	import UploadModal from './UploadModal.svelte';

	// One host for every shell-level modal: ui.modal is a discriminated union, so
	// exactly one can be open and opening any is a single assignment.
	const m = $derived(ui.modal);
	const close = () => (ui.modal = null);
	const staged = () => {
		drafts.refresh();
		ui.showToast('Staged into Changes — applies when the project’s PR merges.', {
			label: 'Review & propose',
			run: () => (ui.changesOpen = true)
		});
	};

	// The per-VM staged-changes modal (opened from a Staged badge).
	let stagedBusy = $state(false);
	const stagedItem = $derived(
		m?.kind === 'staged' ? (drafts.stagedByKey.get(`${m.vm.namespace}/${m.vm.name}`) ?? null) : null
	);
	async function discardStaged() {
		if (m?.kind !== 'staged') return;
		stagedBusy = true;
		try {
			await api.unstage(m.vm.namespace, m.vm.name);
			close();
			await drafts.refresh();
		} catch {
			// Failure leaves the modal open to retry; a 401 signs out centrally.
		} finally {
			stagedBusy = false;
		}
	}
	function reviewStaged() {
		close();
		ui.changesOpen = true;
	}
</script>

{#if m?.kind === 'newVM'}
	<NewVMWizard
		namespaces={m.namespaces ?? inventory.namespaces}
		networks={inventory.networks}
		onclose={close}
		onstaged={staged}
	/>
{:else if m?.kind === 'newNetwork'}
	<NewNetworkModal
		namespaces={inventory.namespaces}
		uplinks={inventory.uplinks}
		canManage={inventory.canManage}
		onAddUplink={() => (ui.modal = { kind: 'uplink' })}
		onclose={close}
		onstaged={staged}
	/>
{:else if m?.kind === 'uplink'}
	<AddUplinkModal adapters={inventory.physicalAdapters} onclose={close} onstaged={staged} />
{:else if m?.kind === 'namespace'}
	<NewNamespaceModal
		projects={inventory.repoProjects}
		project={m.project ?? undefined}
		onclose={close}
		onstaged={staged}
	/>
{:else if m?.kind === 'newProject'}
	<NewProjectModal onclose={close} onstaged={staged} />
{:else if m?.kind === 'adoptProject'}
	<AdoptProjectModal
		project={m.project}
		namespaces={m.namespaces}
		onclose={close}
		onstaged={staged}
	/>
{:else if m?.kind === 'egressFw'}
	<EgressFirewallModal
		namespaces={m.namespaces}
		namespace={m.namespace}
		onclose={close}
		onstaged={staged}
	/>
{:else if m?.kind === 'dfw'}
	<DistributedFirewallModal
		namespaces={m.namespaces}
		namespace={m.namespace}
		vms={inventory.allVMs}
		onclose={close}
		onstaged={staged}
	/>
{:else if m?.kind === 'tier0'}
	<Tier0Modal namespaces={inventory.namespaces} onclose={close} onstaged={staged} />
{:else if m?.kind === 'adminFw'}
	<AdminFirewallModal onclose={close} onstaged={staged} />
{:else if m?.kind === 'upload'}
	<UploadModal namespaces={inventory.namespaces} onclose={close} />
{:else if m?.kind === 'deployTemplate'}
	<DeployTemplateModal
		namespaces={inventory.namespaces}
		library={m.library}
		template={m.template}
		onclose={close}
		onstaged={staged}
	/>
{:else if m?.kind === 'editTemplate'}
	<EditTemplateModal template={m.template} onclose={close} onstaged={staged} />
{:else if m?.kind === 'staged' && stagedItem}
	<StagedChangesModal
		item={stagedItem}
		busy={stagedBusy}
		onclose={close}
		ondiscard={discardStaged}
		onreview={reviewStaged}
	/>
{/if}
