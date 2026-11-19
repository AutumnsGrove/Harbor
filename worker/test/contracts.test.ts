/// <reference types="vite/client" />
import { describe, expect, it } from 'vitest';
import { validate, type SchemaName } from '../src/validate';

interface Fixture {
	schema: SchemaName;
	case: string;
	valid: boolean;
	body: unknown;
	note?: string;
}

// One set of files, two runtimes: the Go side validates the same directory in
// phase 2. A fixture that stops matching its name is the drift alarm.
const fixtures = import.meta.glob<{ default: Fixture }>('../../contracts/fixtures/*.json', { eager: true });

describe('contract fixtures', () => {
	const entries = Object.entries(fixtures);
	it('found fixtures to check', () => {
		expect(entries.length).toBeGreaterThan(10);
	});

	for (const [path, mod] of entries) {
		const fixture = mod.default;
		it(`${path.split('/').pop()} — ${fixture.case} (${fixture.valid ? 'valid' : 'invalid'})`, () => {
			const result = validate(fixture.schema, fixture.body);
			expect(result.valid, JSON.stringify(result.errors)).toBe(fixture.valid);
			if (!fixture.valid) {
				expect(result.errors.length).toBeGreaterThan(0);
				// A rejection is always typed, never a thrown schema error.
				for (const error of result.errors) {
					expect(error.path).toBeTypeOf('string');
					expect(error.keyword).toBeTypeOf('string');
				}
			}
		});
	}
});

describe('validate', () => {
	it('never throws on hostile input', () => {
		const hostile: unknown[] = [null, undefined, 42, 'string', [], { __proto__: { link: 'x' } }, { note: {} }];
		for (const value of hostile) {
			expect(() => validate('save', value)).not.toThrow();
			expect(validate('save', value).valid).toBe(false);
		}
	});

	it('caps the error list', () => {
		const body = { link: 1, note: 2, title: 3 };
		expect(validate('save', body).errors.length).toBeLessThanOrEqual(8);
	});
});
