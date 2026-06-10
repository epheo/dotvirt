// noVNC ships no types. Declare the minimal surface dotvirt uses.
declare module '@novnc/novnc' {
	export default class RFB extends EventTarget {
		constructor(
			target: HTMLElement,
			url: string,
			options?: { credentials?: { password?: string } }
		);
		scaleViewport: boolean;
		background: string;
		disconnect(): void;
	}
}
