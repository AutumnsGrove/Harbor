// The typed client for the Go API. Every non-2xx response becomes an ApiError,
// because the shell has to distinguish "you are not allowed" from "the server is
// broken" instead of showing whichever one the browser guessed.

export class ApiError extends Error {
	readonly status: number;

	constructor(status: number, message: string) {
		super(message);
		this.name = 'ApiError';
		this.status = status;
	}
}

export function isApiError(error: unknown): error is ApiError {
	return error instanceof ApiError;
}

export type VersionResponse = {
	version: string;
	deployment: string;
};

export type AvailableResponse = {
	available: boolean;
	digest?: string;
	version?: string;
};

export type UpdateStatusResponse = {
	status: string;
	version?: string;
	at?: string;
};

/** Both update endpoints answer 202 with the action they accepted. */
export type UpdateActionResponse = {
	status: string;
};

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
	const response = await fetch(path, {
		credentials: 'same-origin',
		...init,
		headers: { accept: 'application/json', ...init.headers }
	});

	if (!response.ok) {
		let message = `${response.status} ${response.statusText}`.trim();
		try {
			const body: unknown = await response.json();
			if (body && typeof body === 'object' && 'error' in body && typeof body.error === 'string') {
				message = body.error;
			}
		} catch {
			// Not JSON (a proxy error page, a truncated response). Keep the status.
		}
		throw new ApiError(response.status, message);
	}

	return (await response.json()) as T;
}

export const api = {
	version: () => request<VersionResponse>('/api/version'),

	update: {
		/** Asks the watcher to resolve `latest`; it writes available.json later. */
		check: () => request<UpdateActionResponse>('/api/update/check', { method: 'POST' }),
		available: () => request<AvailableResponse>('/api/update/available'),
		/** Asks the watcher to apply the pinned digest. Never names an image. */
		start: () => request<UpdateActionResponse>('/api/update/start', { method: 'POST' }),
		status: () => request<UpdateStatusResponse>('/api/update/status')
	}
};
