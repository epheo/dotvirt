<script lang="ts">
	import {
		api,
		DRS_BOUNDS,
		DRS_THRESHOLDS,
		type DRSEnableRequest,
		type DRSMode,
		type DRSView,
	} from '$lib/api';
	import Modal from './Modal.svelte';
	import StageFooter from './StageFooter.svelte';

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
	const valid = $derived(
		inBounds(intervalSeconds, DRS_BOUNDS.intervalSeconds) &&
			inBounds(evictionNodeLimit, DRS_BOUNDS.evictionNodeLimit) &&
			inBounds(evictionTotalLimit, DRS_BOUNDS.evictionTotalLimit),
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
			<span class="text-slate-600">Automation level</span>
			<div class="mt-1 space-y-1">
				<label class="flex items-start gap-2">
					<input type="radio" bind:group={mode} value="Predictive" class="mt-1" />
					<span>
						Predictive
						<span class="block text-xs text-slate-400">
							Dry run: recommendations appear in descheduler logs/metrics, no VM moves.
						</span>
					</span>
				</label>
				<label class="flex items-start gap-2">
					<input type="radio" bind:group={mode} value="Automatic" class="mt-1" />
					<span>
						Automatic
						<span class="block text-xs text-slate-400">
							Fully automated: VMs live-migrate off hot nodes to keep spare capacity even.
						</span>
					</span>
				</label>
			</div>
		</div>

		<label class="block">
			<span class="text-slate-600">Migration aggressiveness</span>
			<select
				bind:value={threshold}
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			>
				{#each DRS_THRESHOLDS as t (t.value)}<option value={t.value}>{t.label} — {t.detail}</option
					>{/each}
			</select>
		</label>

		<label class="block">
			<span class="text-slate-600">Evaluation interval (seconds)</span>
			<input
				type="number"
				min={DRS_BOUNDS.intervalSeconds.min}
				max={DRS_BOUNDS.intervalSeconds.max}
				bind:value={intervalSeconds}
				class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
			/>
		</label>

		<button
			type="button"
			onclick={() => (showAdvanced = !showAdvanced)}
			class="text-xs text-blue-600 hover:underline"
		>
			{showAdvanced ? '− Hide' : '+ Show'} advanced settings
		</button>
		{#if showAdvanced}
			<div class="space-y-3 rounded border border-slate-200 bg-slate-50 p-3">
				<label class="flex items-start gap-2">
					<input type="checkbox" bind:checked={softTainter} class="mt-0.5" />
					<span>
						Soft-taint hot nodes
						<span class="block text-xs text-slate-400">
							Also steer NEW placements away from hot nodes (PreferNoSchedule) until they cool.
						</span>
					</span>
				</label>
				<div class="grid grid-cols-2 gap-3">
					<label class="block">
						<span class="text-slate-600">Max migrations per node</span>
						<input
							type="number"
							min={DRS_BOUNDS.evictionNodeLimit.min}
							max={DRS_BOUNDS.evictionNodeLimit.max}
							bind:value={evictionNodeLimit}
							class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
						/>
					</label>
					<label class="block">
						<span class="text-slate-600">Max migrations cluster-wide</span>
						<input
							type="number"
							min={DRS_BOUNDS.evictionTotalLimit.min}
							max={DRS_BOUNDS.evictionTotalLimit.max}
							bind:value={evictionTotalLimit}
							class="mt-1 w-full rounded border border-slate-300 px-2 py-1.5"
						/>
					</label>
				</div>
				<p class="text-xs text-slate-400">
					Keep at or below the cluster's live-migration limits so DRS never queues more migrations
					than the cluster will run.
				</p>
			</div>
		{/if}

		{#if !view.psiConfigured}
			<label class="flex items-start gap-2 rounded border border-amber-200 bg-amber-50 p-3">
				<input type="checkbox" bind:checked={installPSI} disabled={!view.canPSI} class="mt-0.5" />
				<span class:opacity-50={!view.canPSI}>
					Enable PSI on worker nodes (required for load-aware rebalancing)
					<span class="block text-xs text-amber-700">
						Stages a MachineConfig that <strong>reboots every worker node</strong> when the PR merges.
						Skip if PSI (psi=1) is already enabled out-of-band.
					</span>
					{#if !view.canPSI}
						<span class="block text-xs text-slate-400">
							Requires MachineConfig authoring permission.
						</span>
					{/if}
				</span>
			</label>
		{/if}

		{#if error}
			<pre class="rounded bg-red-50 p-3 text-xs whitespace-pre-wrap text-red-700">{error}</pre>
		{/if}
	</div>

	{#snippet footer()}
		<StageFooter
			{submitting}
			disabled={!valid}
			label={view.configured ? 'Stage changes' : 'Stage DRS enablement'}
			onsubmit={submit}
			oncancel={onclose}
		/>
	{/snippet}
</Modal>
