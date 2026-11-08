// noVNC ships no types. Declare the minimal surface dotvirt uses.
declare module '@novnc/novnc' {
	export default class RFB extends EventTarget {
		constructor(
			target: HTMLElement,
			// A URL, or a socket the caller opened to watch its close itself.
			url: string | WebSocket,
			options?: { credentials?: { password?: string } },
		);
		scaleViewport: boolean;
		background: string;
		disconnect(): void;
	}
}
