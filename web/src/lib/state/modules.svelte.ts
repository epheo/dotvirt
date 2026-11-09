import type { Modules } from '#lib/api.js';

// GET /api/modules. Only a known no hides anything: null (not read yet, or the
// read failed) keeps the console as it is.
class ModulesStore {
	value = $state.raw<Modules | null>(null);
}
export const modules = new ModulesStore();

export const vmsOff = () => modules.value !== null && !modules.value.vms;
export const computeOff = () => vmsOff() && !modules.value?.desktops;
export const networksOff = () => modules.value !== null && !modules.value.networks;

// What Compute's root is called: projects exist whatever the cluster runs.
export const computeRoot = () => (vmsOff() ? 'All projects' : 'All VMs');
