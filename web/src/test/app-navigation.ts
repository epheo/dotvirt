// Stands in for $app/navigation under vitest (aliased in vitest.config.ts):
// records each goto so a test can assert where an action landed.
export const navigations: string[] = [];

export async function goto(url: string | URL): Promise<void> {
	navigations.push(url.toString());
}
