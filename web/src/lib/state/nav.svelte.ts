import type { Section } from '$lib/nav';
import { persisted } from './persisted.svelte';

// Where "home" (/) lands: the last inventory section the user was in.
const SECTIONS: Section[] = ['compute', 'hosts', 'networking', 'storage', 'catalog'];
export const lastSection = persisted<Section>('dotvirt.nav.section', 'compute');
if (!SECTIONS.includes(lastSection.value)) lastSection.value = 'compute';
