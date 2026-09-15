import type { Attachment } from 'svelte/attachments';

// The one click-away idiom for menus, popovers and the search results: a
// click or right-click outside the node, or Escape, closes it. The listeners
// sit on window at bubble phase, so the trigger's own onclick runs first and
// a toggle shut stays shut. Events older than the attachment are ignored:
// the right-click that opened a context menu is still bubbling when the menu
// mounts.
export function dismiss(onclose: () => void): Attachment<HTMLElement> {
	return (node) => {
		const since = performance.now();
		const away = (e: Event) => {
			if (e.timeStamp > since && !node.contains(e.target as Node)) onclose();
		};
		const key = (e: KeyboardEvent) => {
			if (e.key === 'Escape') onclose();
		};
		window.addEventListener('click', away);
		window.addEventListener('contextmenu', away);
		window.addEventListener('keydown', key);
		return () => {
			window.removeEventListener('click', away);
			window.removeEventListener('contextmenu', away);
			window.removeEventListener('keydown', key);
		};
	};
}
