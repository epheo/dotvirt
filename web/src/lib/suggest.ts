// A suggestion is a default the form pushes when the field stays empty, shown
// in place as the placeholder. Tab on the empty field takes it as the value and
// keeps focus, so the user can edit it before moving on. Shift+Tab is left
// alone: walking backwards must never fill fields.
export function acceptsSuggestion(e: KeyboardEvent, value: unknown, suggest?: string): boolean {
	if (e.key !== 'Tab' || e.shiftKey || !suggest) return false;
	if (value !== '' && value !== null && value !== undefined) return false;
	e.preventDefault();
	return true;
}
