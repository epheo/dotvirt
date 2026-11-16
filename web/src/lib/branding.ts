// isConsoleLink reports a masthead link that leads back to this console: the
// same origin as the page. The masthead marks it as the current item instead
// of linking to itself. An unparsable URL is just a link.
export function isConsoleLink(url: string, origin: string): boolean {
	try {
		return new URL(url).origin === origin;
	} catch {
		return false;
	}
}
