import { Validator } from '@cfworker/json-schema';
import saveSchema from '../../contracts/save.schema.json';
import uploadSchema from '../../contracts/upload.schema.json';
import jobSchema from '../../contracts/job.schema.json';

// The schemas are the ones in contracts/, imported as JSON so there is one set of
// files for both runtimes. @cfworker/json-schema interprets them; Ajv's default
// code generation is forbidden in workerd (S1: `new Function` → EvalError).

export type SchemaName = 'save' | 'upload' | 'job';

const schemas: Record<SchemaName, unknown> = {
	save: saveSchema,
	upload: uploadSchema,
	job: jobSchema,
};

// Validators compile once per isolate: constructing one walks the schema.
const validators = new Map<SchemaName, Validator>();

function validator(name: SchemaName): Validator {
	let v = validators.get(name);
	if (!v) {
		v = new Validator(schemas[name] as never, '2020-12', false);
		validators.set(name, v);
	}
	return v;
}

export interface ValidationError {
	path: string;
	keyword: string;
	message: string;
}

export interface ValidationResult {
	valid: boolean;
	errors: ValidationError[];
}

/**
 * Validate an object. Never throws on bad input — a malformed body is a typed
 * 400, not a 500 (the plan's fuzz property).
 */
export function validate(name: SchemaName, value: unknown): ValidationResult {
	let result;
	try {
		result = validator(name).validate(value);
	} catch {
		return { valid: false, errors: [{ path: '#', keyword: 'internal', message: 'schema rejected the value' }] };
	}
	if (result.valid) return { valid: true, errors: [] };
	return {
		valid: false,
		// Cap the list: a 400 does not need to enumerate a thousand errors.
		errors: result.errors.slice(0, 8).map((e) => ({
			path: e.instanceLocation || '#',
			keyword: e.keyword,
			message: e.error,
		})),
	};
}
