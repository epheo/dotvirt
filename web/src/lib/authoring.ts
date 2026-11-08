// A namespace's authoring verdict as the frame carries it: the server's yes,
// its no, or unknown when its review failed this frame. Both closed verdicts
// offer nothing, since the stage routes enforce the same check; they differ
// only in what they tell the person: wait, or ask for access.
export type Authoring = 'yes' | 'no' | 'unknown';

export const NOT_IN_GIT = 'Not in git: adopt this VM first';
export const READ_ONLY = 'Read-only: you may not edit VMs in this namespace';
export const ACCESS_UNKNOWN = 'Your access could not be checked. Try again in a moment.';
