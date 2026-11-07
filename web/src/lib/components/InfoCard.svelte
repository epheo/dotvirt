<script lang="ts">
	import type { Snippet } from 'svelte';
	import Card from './Card.svelte';

	// The read-only info card: a flush Card whose body is the caller's,
	// typically a <dl> of <Row>s or a divided list, with an optional header
	// action (a configure tab's Edit link).
	let { title, action, children }: { title: string; action?: Snippet; children: Snippet } =
		$props();
</script>

<Card {title} actions={action} flush>{@render children()}</Card>
