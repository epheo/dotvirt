<script lang="ts">
	import {
		api,
		DRS_BOUNDS,
		DRS_THRESHOLDS,
		type DRSEnableRequest,
		type DRSMode,
		type DRSView,
	} from '$lib/api';
	import ChoiceCards from './ChoiceCards.svelte';
	import ErrorNote from './ErrorNote.svelte';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';
	import FormField from './FormField.svelte';
	import TextInput from './TextInput.svelte';
	import SelectInput from './SelectInput.svelte';

	// The vSphere-DRS dialog, GitOps-shaped: every choice renders into the
	// KubeDescheduler manifest set staged into the platform draft — nothing
	// touches the cluster until the PR merges.
	let {
		view,
		onclose,
		onstaged,
	}: {
		view: DRSView; // current state, to seed the form
		onclose: () => void;
		onstaged: () => void;
	} = $props();

	// Seed from the pending draft when one is staged — editing an unproposed
	// change continues it (PSI opt-in included) — else the committed config.
	// The modal is mounted fresh per open.
	// svelte-ignore state_referenced_locally
	const cfg = view.draft?.config ?? view.config;
	let mode = $state<DRSMode>(cfg?.mode ?? 'Automatic');
	let threshold = $state(cfg?.threshold ?? 'AsymmetricLow');
	let intervalSeconds = $state(cfg?.intervalSeconds ?? 60);
	let softTainter = $state(cfg?.softTainter ?? true);
	let evictionNodeLimit = $state(cfg?.evictionNodeLimit ?? 2);
	let evictionTotalLimit = $state(cfg?.evictionTotalLimit ?? 5);
	// svelte-ignore state_referenced_locally
	let installPSI = $state(view.draft?.psi ?? false);
	let showAdvanced = $state(false);

	let submitting = $state(false);
	let error = $state('');

	const inBounds = (v: number, b: { min: number; max: number }) => v >= b.min && v <= b.max;
	const missing = $derived.by(() => {
		const m: string[] = [];
		const b = DRS_BOUNDS;
		if (!inBounds(intervalSeconds, b.intervalSeconds))
			m.push(`Interval must be ${b.intervalSeconds.min}-${b.intervalSeconds.max}s`);
		if (!inBounds(evictionNodeLimit, b.evictionNodeLimit))
			m.push(`Per-node limit must be ${b.evictionNodeLimit.min}-${b.evictionNodeLimit.max}`);
		if (!inBounds(evictionTotalLimit, b.evictionTotalLimit))
			m.push(`Cluster-wide limit must be ${b.evictionTotalLimit.min}-${b.evictionTotalLimit.max}`);
		return m;
	});
	const valid = $derived(missing.length === 0);
	const summary = $derived(
		valid
			? `Stages ${mode} DRS (${threshold}, every ${intervalSeconds}s)${installPSI ? ' + PSI MachineConfig' : ''} → platform repo`
			: '',
	);

	async function submit() {
		if (!valid) return;
		submitting = true;
		error = '';
		const req: DRSEnableRequest = {
			mode,
			threshold,
			intervalSeconds,
			softTainter,
			evictionNodeLimit,
			evictionTotalLimit,
		};
		if (installPSI) req.installPSI = true;
		try {
			await api.enableDRS(req);
			onstaged();
			onclose();
		} catch (e) {
			error = String(e);
		} finally {
			submitting = false;
		}
	}
</script>

<Modal title="Configure DRS" size="lg" {onclose}>
	<div class="space-y-4 overflow-y-auto px-5 py-4 text-sm">
		<div>
			<span class="mb-1 block text-ink-soft">Automation level</span>
			<ChoiceCards
				options={[
					{
						value: 'Predictive',
						label: 'Predictive',
						hint: 'Dry run: recommendations in logs/metrics, no VM moves',
					},
					{
						value: 'Automatic',
						label: 'Automatic',
						hint: 'VMs live-migrate off hot nodes to keep spare capacity even',
					},
				]}
				bind:value={mode}
			/>
		</div>

		<FormField label="Migration aggressiveness">
			<SelectInput bind:value={threshold}>
				{#each DRS_THRESHOLDS as t (t.value)}<option value={t.value}>{t.label} — {t.detail}</option
					>{/each}
			</SelectInput>
		</FormField>

		<FormField label="Evaluation interval (seconds)">
			<TextInput
				type="number"
				min={DRS_BOUNDS.intervalSeconds.min}
				max={DRS_BOUNDS.intervalSeconds.max}
				bind:value={intervalSeconds}
			/>
		</FormField>

		<button
			type="button"
			onclick={() => (showAdvanced = !showAdvanced)}
			class="text-xs text-accent hover:underline"
		>
			{showAdvanced ? '− Hide' : '+ Show'} advanced settings
		</button>
		{#if showAdvanced}
			<div class="space-y-3 rounded border border-line bg-inset p-3">
				<label class="flex items-start gap-2">
					<input type="checkbox" bind:checked={softTainter} class="mt-0.5" />
					<span>
						Soft-taint hot nodes
						<span class="block text-xs text-ink-faint">
							Also steer NEW placements away from hot nodes (PreferNoSchedule) until they cool.
						</span>
					</span>
				</label>
				<div class="grid grid-cols-2 gap-3">
					<FormField label="Max migrations per node">
						<TextInput
							type="number"
							min={DRS_BOUNDS.evictionNodeLimit.min}
							max={DRS_BOUNDS.evictionNodeLimit.max}
							bind:value={evictionNodeLimit}
						/>
					</FormField>
					<FormField label="Max migrations cluster-wide">
						<TextInput
							type="number"
							min={DRS_BOUNDS.evictionTotalLimit.min}
							max={DRS_BOUNDS.evictionTotalLimit.max}
							bind:value={evictionTotalLimit}
						/>
					</FormField>
				</div>
				<p class="text-xs text-ink-faint">
					Keep at or below the cluster's live-migration limits so DRS never queues more migrations
					than the cluster will run.
				</p>
			</div>
		{/if}

		{#if !view.psiConfigured}
			<label class="flex items-start gap-2 rounded border border-warn-soft bg-warn-soft/60 p-3">
				<input type="checkbox" bind:checked={installPSI} disabled={!view.canPSI} class="mt-0.5" />
				<span class:opacity-50={!view.canPSI}>
					Enable PSI on worker nodes (required for load-aware rebalancing)
					<span class="block text-xs text-warn-ink">
						Stages a MachineConfig that <strong>reboots every worker node</strong> when the PR merges.
						Skip if PSI (psi=1) is already enabled out-of-band.
					</span>
					{#if !view.canPSI}
						<span class="block text-xs text-ink-faint">
							Requires MachineConfig authoring permission.
						</span>
					{/if}
				</span>
			</label>
		{/if}

		<ErrorNote {error} />
	</div>

	{#snippet footer()}
		<StageFooter
			{submitting}
			disabled={!valid}
			{missing}
			{summary}
			label={view.configured ? 'Stage changes' : 'Stage DRS enablement'}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
