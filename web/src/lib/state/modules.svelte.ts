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
// What the cluster can install, a fact about it, never a missing right:
// rebalancing's operator comes through OLM, its PSI option is a MachineConfig.
export const olmOff = () => modules.value !== null && !modules.value.olm;
export const machineConfigOff = () => modules.value !== null && !modules.value.machineConfig;

// What Compute's root is called: projects exist whatever the cluster runs.
export const computeRoot = () => (vmsOff() ? 'All projects' : 'All VMs');
