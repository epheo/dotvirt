<script lang="ts">
	import { api, HA_BOUNDS, type HAEnableRequest, type HAView } from '$lib/api';
	import StageModal from './StageModal.svelte';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';

	// The vSphere-HA dialog, GitOps-shaped: every choice renders into the
	// NodeHealthCheck manifest set staged into the platform draft - nothing
	// touches the cluster until the PR merges.
	let {
		view,
		onclose,
		onstaged,
	}: {
		view: HAView; // current state, to seed the form
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	// Seed from the pending draft when one is staged - editing an unproposed
	// change continues it - else the committed config. Mounted fresh per open.
	// svelte-ignore state_referenced_locally
	const cfg = view.draft?.config ?? view.config;
	let unhealthySeconds = $state(cfg?.unhealthySeconds ?? 300);
	let minHealthyPercent = $state(cfg?.minHealthyPercent ?? 51);

	const inBounds = (v: number, b: { min: number; max: number }) => v >= b.min && v <= b.max;
	const missing = $derived.by(() => {
		const m: string[] = [];
		const b = HA_BOUNDS;
		if (!inBounds(unhealthySeconds, b.unhealthySeconds))
			m.push(`Detection must be ${b.unhealthySeconds.min}-${b.unhealthySeconds.max}s`);
		if (!inBounds(minHealthyPercent, b.minHealthyPercent))
			m.push(`Minimum healthy must be ${b.minHealthyPercent.min}-${b.minHealthyPercent.max}%`);
		return m;
	});
	const valid = $derived(missing.length === 0);
	const summary = $derived(
		valid
			? `Stages host fencing + VM restart (fence after ${unhealthySeconds}s, halt below ${minHealthyPercent}% healthy) -> platform repo`
			: '',
	);

	async function stage() {
		const req: HAEnableRequest = { unhealthySeconds, minHealthyPercent };
		await api.enableHA(req);
	}
</script>

<StageModal
	title="Configure High Availability"
	label={view.configured ? 'Stage changes' : 'Stage High Availability'}
	{missing}
	{summary}
	onsubmit={stage}
	{onstaged}
	{onclose}
>
	<FormField label="Host failure detection (seconds)">
		<TextInput
			type="number"
			min={HA_BOUNDS.unhealthySeconds.min}
			max={HA_BOUNDS.unhealthySeconds.max}
			bind:value={unhealthySeconds}
		/>
		<p class="mt-1 text-xs text-ink-faint">
			How long a host must stay unresponsive before it is fenced. Too short fences hosts over
			transient network blips.
		</p>
	</FormField>

	<FormField label="Minimum healthy hosts (%)">
		<TextInput
			type="number"
			min={HA_BOUNDS.minHealthyPercent.min}
			max={HA_BOUNDS.minHealthyPercent.max}
			bind:value={minHealthyPercent}
		/>
		<p class="mt-1 text-xs text-ink-faint">
			Fencing halts when fewer than this share of hosts are healthy — the brake against remediating
			a whole cluster over a shared failure.
		</p>
	</FormField>

	<p class="rounded border border-warn-soft bg-warn-soft/60 p-3 text-xs text-warn-ink">
		When a host stays unresponsive past the detection window it is <strong>rebooted</strong> and its VMs
		restart on surviving hosts. Control-plane nodes are never fenced.
	</p>
</StageModal>
