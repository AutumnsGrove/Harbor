import { describe, expect, it } from 'vitest';

import { coverRatio, lightAt, seedOf } from './art';

describe('art', () => {
	it('gives the same item the same look every time', () => {
		expect(seedOf('5898bac2-387f-403d-89a8-dd17dfe33d9f')).toBe(
			seedOf('5898bac2-387f-403d-89a8-dd17dfe33d9f')
		);
	});

	// The reason the hash is mixed: ids saved seconds apart differ in a few
	// characters, and neighbours in the feed must not all share one proportion.
	it('spreads near-identical ids across more than one proportion', () => {
		const shapes = new Set<string>();
		for (let i = 0; i < 40; i++) {
			const id = `0194f2c0-7b3a-7c10-9e5d-00000000${i.toString(16).padStart(4, '0')}`;
			shapes.add(coverRatio('article', seedOf(id)));
		}
		expect(shapes.size).toBeGreaterThanOrEqual(4);
	});

	it('weights shape by kind: a video is never taller than it is wide', () => {
		for (let i = 0; i < 40; i++) {
			const [w, h] = coverRatio('video', seedOf(`v-${i}`)).split(' / ').map(Number);
			expect(w).toBeGreaterThan(h ?? Infinity);
		}
	});

	it('always yields a valid light position, never a negative percentage', () => {
		for (let i = 0; i < 200; i++) {
			const { x, y } = lightAt(seedOf(`item-${i}`));
			expect(Number.parseInt(x, 10)).toBeGreaterThanOrEqual(0);
			expect(Number.parseInt(y, 10)).toBeGreaterThanOrEqual(0);
		}
	});
});
