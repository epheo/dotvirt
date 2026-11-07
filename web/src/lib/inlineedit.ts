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

// Staging replaces a VM's whole entry, so one field cannot be staged alone
// without dropping the rest: the edit to send is what is staged, with this
// field set, or without it once it is back at git's value. null means the
// result stages nothing, so the entry should be unstaged instead.
export function composeEdit(
	staged: Edit | undefined,
	field: InlineField,
	value: InlineValue,
	gitValue: InlineValue | undefined,
): Edit | null {
	const next: Edit = { ...(staged ?? {}) };
	if (value === gitValue) delete next[field];
	else (next as Record<InlineField, InlineValue>)[field] = value;
	return Object.values(next).some((v) => v !== undefined && v !== null && !emptyList(v))
		? next
		: null;
}

const emptyList = (v: unknown) =>
	(Array.isArray(v) && v.length === 0) ||
	(typeof v === 'object' && v !== null && !Array.isArray(v) && Object.keys(v).length === 0);

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
	return '';
}
