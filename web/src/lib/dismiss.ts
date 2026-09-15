import type { Attachment } from 'svelte/attachments';

// The one click-away idiom for menus, popovers and the search results: a
// click or right-click outside the node, or Escape, closes it. The first
// outside click only closes - it is swallowed at capture phase so a grid row
// under it is not also selected, as the backdrop this replaced did. An
// outside right-click closes without being swallowed, so the row's own
// handler opens its menu in the same gesture. Events older than the
// attachment are ignored: the right-click that opened a context menu is
// still bubbling when the menu mounts. Attach only while open; a closed
// popover must not eat the page's clicks.
export function dismiss(onclose: () => void): Attachment<HTMLElement> {
	return (node) => {
		const since = performance.now();
		const outside = (e: Event) => e.timeStamp > since && !node.contains(e.target as Node);
		const click = (e: Event) => {
			if (!outside(e)) return;
			e.preventDefault();
			e.stopPropagation();
			onclose();
		};
		const contextmenu = (e: Event) => {
			if (outside(e)) onclose();
		};
		const key = (e: KeyboardEvent) => {
			if (e.key === 'Escape') onclose();
		};
		window.addEventListener('click', click, true);
		window.addEventListener('contextmenu', contextmenu, true);
		window.addEventListener('keydown', key);
		return () => {
			window.removeEventListener('click', click, true);
			window.removeEventListener('contextmenu', contextmenu, true);
			window.removeEventListener('keydown', key);
		};
	};
}
