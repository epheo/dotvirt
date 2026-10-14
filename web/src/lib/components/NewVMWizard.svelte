<script lang="ts">
	import { untrack } from 'svelte';
	import { api, type CreateVMRequest, type Network } from '#lib/api.js';
	import { attachableNetworks } from '#lib/networks.js';
	import { rowList } from '#lib/rowlist.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import Modal from './Modal.svelte';
	import Wizard from './Wizard.svelte';
	import NameStep from './newvm/NameStep.svelte';
	import GuestOSStep from './newvm/GuestOSStep.svelte';
	import ComputeStep from './newvm/ComputeStep.svelte';
	import StorageStep from './newvm/StorageStep.svelte';
	import NetworksStep from './newvm/NetworksStep.svelte';
	import CustomizeStep, { generatePassword } from './newvm/CustomizeStep.svelte';
	import ReviewStep from './newvm/ReviewStep.svelte';
	import CredentialsReveal from './newvm/CredentialsReveal.svelte';

	let {
		namespaces,
		namespace: initialNamespace = '',
		networks = [],
		onclose,
	}: {
		namespaces: string[];
		namespace?: string; // preselected target (the opener's scope)
		networks?: Network[]; // port-group catalog (from the page), for the adapter picker
		onclose: () => void;
	} = $props();

	// Re-pulls only after a boot-time failure, so opening the dialog is the retry.
	inventory.loadOptions();
	const options = $derived(inventory.options);

	let name = $state('');
	// Default to the first project; seeded in the mount effect to avoid capturing
	// only the initial prop value.
	let namespace = $state('');
	let osImage = $state(''); // "name|namespace"
	let instancetype = $state('');
	let preference = $state('');
	let diskSize = $state('30Gi');
	let storageClass = $state(''); // '' = cluster default
	let running = $state(true);
	let sshKey = $state('');
	let user = $state('');
	// Generated up front so every VM boots console-reachable (cloud images ship
	// with locked passwords). Git keeps only a bcrypt hash: the plaintext exists
	// here and in the post-stage reveal, then nowhere.
	let password = $state(generatePassword());
	// A successful stage with a password swaps the wizard for the one-time
	// credentials reveal instead of closing.
	let revealed = $state(false);
	function staged() {
		if (password) revealed = true;
		else onclose();
	}
	const disks = rowList((n) => ({ name: `disk${n + 1}`, size: '10Gi', storageClass: '' }), [], {
		empty: true,
	});
	const extraDisks = $derived(disks.rows);
	// Selected secondary networks, held as attach refs ("namespace/nad", or a bare
	// name for a shared CUDN).
	let selectedNetworks = $state<string[]>([]);
	// Attach the primary (pod-network) NIC. On by default, but - unlike a pod - a VM
	// may decline it and run on secondary networks alone.
	let attachPrimary = $state(true);

	// The active wizard step (bound into <Wizard>); the review step's Edit links
	// seek it back to a specific step.
	let current = $state(0);

	// Attachable secondary port groups for the chosen project: shared (CUDN)
	// networks plus this namespace's own non-default networks. The primary
	// ("VM Network") is excluded here - it's offered as its own toggle below.
	const available = $derived(attachableNetworks(networks, namespace));
	// The project's primary ("VM Network") UDN, when one exists - else the primary
	// NIC is the cluster default pod network, shown with a generic label.
	const primaryNet = $derived(
		networks.find((n) => n.kind === 'default' && n.namespace === namespace),
	);
	const primaryLabel = $derived(primaryNet?.name ?? 'Pod network');
	// Project networks are namespace-bound, so clear the selection when the target
	// project changes (a stale pick from another project mustn't carry over); the
	// primary NIC resets to its attached default.
	$effect(() => {
		namespace;
		selectedNetworks = [];
		attachPrimary = true;
	});

	// Conventional first-boot login per image family; a prefill, freely editable.
	function defaultUser(image: string): string {
		if (image.includes('fedora')) return 'fedora';
		if (image.includes('ubuntu')) return 'ubuntu';
		if (image.includes('debian')) return 'debian';
		return 'cloud-user';
	}

	// Follow the image's conventional user until the field is hand-edited.
	let userAuto = '';
	$effect(() => {
		const auto = defaultUser(osImage.split('|')[0].toLowerCase());
		untrack(() => {
			if (user === '' || user === userAuto) user = auto;
			userAuto = auto;
		});
	});

	// Sensible defaults from what's available, seeded once the catalog lands
	// and only into fields still blank. Guard each list - a source the backend
	// SA can't read comes back empty (or null on an old build).
	$effect(() => {
		const o = inventory.options;
		if (!o) return;
		untrack(() => {
			const osImages = o.osImages ?? [];
			const fed =
				osImages.find((i) => i.ready && i.name === 'fedora') ?? osImages.find((i) => i.ready);
			if (fed && !osImage) osImage = `${fed.name}|${fed.namespace}`;
			if (!preference)
				preference =
					(o.preferences ?? []).find((p) => p.name === 'fedora')?.name ??
					o.preferences?.[0]?.name ??
					'';
			if (!instancetype)
				instancetype =
					(o.instancetypes ?? []).find((i) => i.name === 'u1.medium')?.name ??
					o.instancetypes?.[0]?.name ??
					'';
		});
	});

	// A VM needs at least one NIC: the primary, a secondary, or both.
	const hasNetwork = $derived(attachPrimary || selectedNetworks.length > 0);
	// Global gate: reused as both the Finish gate and submit()'s guard.
	const valid = $derived(
		!!(name && namespace && osImage && instancetype && preference) && hasNetwork,
	);
	// Per-step validity - drives only the rail markers, never blocks navigation.
	const step1Valid = $derived(!!(name && namespace));
	const step2Valid = $derived(!!(osImage && preference));
	const step3Valid = $derived(!!instancetype);

	// Steps the user left while incomplete: on return (rail click, Back, or a
	// review-step jump) their fields carry inline errors - the amber rail badge
	// alone names no field.
	let nudged = $state<Set<number>>(new Set());
	let prevStep = 0;
	$effect(() => {
		const cur = current;
		if (cur === prevStep) return;
		if ([step1Valid, step2Valid, step3Valid][prevStep] === false)
			nudged = new Set([...nudged, prevStep]);
		prevStep = cur;
	});

	function submit() {
		const [imgName, imgNs] = osImage.split('|');
		const req: CreateVMRequest = {
			name,
			namespace,
			instancetype,
			preference,
			osImage: { name: imgName, namespace: imgNs },
			diskSize,
			storageClass: storageClass || undefined,
			running,
			extraDisks: extraDisks.length
				? extraDisks.map((d) => ({
						name: d.name,
						size: d.size,
						storageClass: d.storageClass || undefined,
					}))
				: undefined,
			networks: selectedNetworks.length ? selectedNetworks.map((n) => ({ name: n })) : undefined,
			primaryNetwork: attachPrimary,
		};
		if (user || sshKey || password)
			req.cloudInit = {
				user: user || undefined,
				sshKey: sshKey || undefined,
				password: password || undefined,
			};
		return api.stageCreate(req);
	}
</script>

{#snippet step1()}
	<NameStep bind:name bind:namespace {namespaces} {initialNamespace} nudged={nudged.has(0)} />
{/snippet}

{#snippet step2()}
	<GuestOSStep bind:osImage bind:preference {options} nudged={nudged.has(1)} />
{/snippet}

{#snippet step3()}
	<ComputeStep bind:instancetype bind:running {options} nudged={nudged.has(2)} />
{/snippet}

{#snippet step4()}
	<StorageStep bind:diskSize bind:storageClass {disks} {options} />
{/snippet}

{#snippet step5()}
	<NetworksStep
		bind:attachPrimary
		bind:selectedNetworks
		{available}
		{primaryLabel}
		{namespace}
		{hasNetwork}
	/>
{/snippet}

{#snippet step6()}
	<CustomizeStep bind:user bind:password bind:sshKey />
{/snippet}

{#snippet review()}
	<ReviewStep
		{name}
		{namespace}
		{osImage}
		{preference}
		{instancetype}
		{running}
		{diskSize}
		{storageClass}
		{extraDisks}
		{attachPrimary}
		{primaryLabel}
		{selectedNetworks}
		{hasNetwork}
		{user}
		{password}
		{sshKey}
		{networks}
		{options}
		onedit={(step) => (current = step)}
	/>
{/snippet}

{#if revealed}
	<CredentialsReveal {name} {user} {password} {onclose} />
{:else if inventory.optionsError}
	<Modal title="New Virtual Machine" {onclose}>
		<div class="px-5 py-4">
			<p class="rounded bg-danger-soft/60 px-3 py-2 text-sm text-danger-ink">
				Failed to load options: {inventory.optionsError}
			</p>
		</div>
	</Modal>
{:else if !options}
	<Modal title="New Virtual Machine" {onclose}>
		<div class="px-5 py-4">
			<p class="text-sm text-ink-faint">Loading cluster options…</p>
		</div>
	</Modal>
{:else}
	<Wizard
		title="New Virtual Machine"
		footerHint="Staged into Changes; open a PR from there."
		finishLabel="Stage VM"
		canFinish={valid}
		bind:current
		onsubmit={submit}
		onsuccess={staged}
		{onclose}
		steps={[
			{ title: 'Name and project', valid: step1Valid, body: step1 },
			{ title: 'Guest OS', valid: step2Valid, body: step2 },
			{ title: 'Compute', valid: step3Valid, body: step3 },
			{ title: 'Storage', body: step4 },
			{ title: 'Networks', body: step5 },
			{ title: 'Customize', body: step6 },
			{ title: 'Ready to complete', body: review },
		]}
	/>
{/if}
