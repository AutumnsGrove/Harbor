import { describe, expect, it } from 'vitest';

import { coverRatio, lightAt, seedOf } from './art';

describe('art', () => {
	it('gives the same item the same look every time', () => {
		expect(seedOf('5898bac2-387f-403d-89a8-dd17dfe33d9f')).toBe(
			seedOf('5898bac2-387f-403d-89a8-dd17dfe33d9f')
		);
	});

	// Item ids are random v4 UUIDs, so this builds the same shape from a seeded
	// generator: the same 2,000 ids on every run, no flake.
	function uuidLikes(count: number): string[] {
		let state = 123456789;
		const word = () => {
			state = (Math.imul(state, 1664525) + 1013904223) >>> 0;
			return state.toString(16).padStart(8, '0');
		};
		return Array.from({ length: count }, () => {
			const [a, b, c, d] = [word(), word(), word(), word()] as [string, string, string, string];
			return `${a}-${b.slice(0, 4)}-4${b.slice(4, 7)}-8${c.slice(0, 3)}-${c.slice(3)}${d.slice(0, 7)}`;
		});
	}

	// The property the masonry depends on: over many ids each of the seven
	// proportions turns up about equally often. This catches a seed that is
	// degenerate or biased (a hash that collapses to one shape fails it outright);
	// FNV-1a sits within a few per cent of even, so the 40% band has plenty of
	// room. The old h * 31 + c hash is caught by the next test, not this one.
	it('spreads random ids evenly across every proportion', () => {
		const ids = uuidLikes(2000);
		const counts = new Map<string, number>();
		for (const id of ids) {
			const shape = coverRatio('article', seedOf(id));
			counts.set(shape, (counts.get(shape) ?? 0) + 1);
		}

		expect(counts.size).toBe(7);
		const expected = ids.length / counts.size;
		for (const [shape, count] of counts) {
			expect(count, shape).toBeGreaterThan(expected * 0.6);
			expect(count, shape).toBeLessThan(expected * 1.4);
		}
	});

	// Ids that are neighbours in time (a time-ordered id scheme would give these)
	// differ in a few trailing characters; they must still land on different
	// shapes. The first hash managed 4 of 7 here.
	it('separates ids that differ only in their last characters', () => {
		const shapes = new Set<string>();
		for (let i = 0; i < 40; i++) {
			const id = `0194f2c0-7b3a-7c10-9e5d-00000000${i.toString(16).padStart(4, '0')}`;
			shapes.add(coverRatio('article', seedOf(id)));
		}
		expect(shapes.size).toBeGreaterThanOrEqual(6);
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
