// Stories in Component Story Format 3 (CSF3), the format Storybook reads, so
// adding Storybook later needs no rewrite. Until then components.test.tsx
// renders every story, so each one is also a smoke test.
import type { ComponentType } from "react";

export type Meta<P> = { title: string; component: ComponentType<P> };
export type Story<P> = { name?: string; args: P };
