import type { DraftItem, EditRequest, VM } from '#lib/api.js';

// The fields a row of the Configuration card edits in place.
export type InlineField = 'power' | 'cpuCores' | 'memory';
export type InlineValue = string | number;
type Edit = Omit<EditRequest, 'sourceFile'>;

// The change labels the backend writes for each field (manifest.ChangesForEdit).
export const FIELD_LABEL: Record<InlineField, string> = {
	power: 'Power',
	cpuCores: 'CPU',
	memory: 'Memory',
};

// The edit staging one field sends. The backend lays it over what is staged
// for the VM and drops a field back at git's value, so sending git's value
// unstages the field; a value git leaves unset is sent as its zero.
export function fieldEdit(field: InlineField, value: InlineValue | undefined): Edit {
	const zero = field === 'cpuCores' ? 0 : '';
	return { [field]: field === 'cpuCores' ? Number(value ?? zero) : (value ?? zero) } as Edit;
}

// Why a row cannot be edited in place, or '' when it can. A staged create,
// delete or whole-file edit has no field model to extend, and an instance type
// owns CPU and memory.
export function inlineBlock(
	vm: VM,
	field: InlineField,
	staged: DraftItem | null | undefined,
): string {
	if (!vm.sourceFile) return 'Not in git: adopt this VM first';
	if (staged && (staged.kind !== 'edit' || !staged.edit))
		return `A staged ${staged.kind} of this VM is waiting; review it in Changes first`;
	if (field !== 'power' && vm.instancetype)
		return `Sized by instance type ${vm.instancetype}; change it in Edit settings`;
	const switching = staged?.edit?.sizing === 'instancetype' || !!staged?.edit?.instancetype;
	if (field !== 'power' && switching)
		return `A staged switch to instance type ${staged?.edit?.instancetype ?? ''} owns CPU and memory; change it in Edit settings`;
	return '';
}
