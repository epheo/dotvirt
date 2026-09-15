import { INVENTORY_SECTIONS, type Section } from '$lib/nav';
import { persisted } from './persisted.svelte';

// Where "home" (/) lands: the last inventory section the user was in.
export const lastSection = persisted<Section>('dotvirt.nav.section', 'compute');
if (!INVENTORY_SECTIONS.includes(lastSection.value)) lastSection.value = 'compute';
