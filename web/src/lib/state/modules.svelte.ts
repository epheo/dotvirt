import type { Modules } from '#lib/api.js';
import { clusterGate } from '#lib/gates.js';
import type { Section } from '#lib/nav.js';
import { CHECKING } from './caps.svelte';

// GET /api/modules. Only a known no hides anything: null (not read yet, or the
// read failed) keeps the console as it is.
class ModulesStore {
	value = $state.raw<Modules | null>(null);
}
export const modules = new ModulesStore();

export const vmsOff = () => modules.value !== null && !modules.value.vms;
export const computeOff = () => vmsOff() && !modules.value?.desktops;
export const networksOff = () => modules.value !== null && !modules.value.networks;

// Where a caller lands when Compute is off; null while the Hosts answer is out,
// or a caller allowed Hosts would land past it.
export function firstSection(): Section | null {
	const hosts = clusterGate.hosts();
	if (hosts.why === CHECKING) return null;
	return hosts.ok ? 'hosts' : 'networking';
}
