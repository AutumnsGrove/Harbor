// The shell is a static SPA served by the Go binary: no SSR, no Node at
// runtime, and the Go server owns the routes it does not recognise (design.md §3).
export const ssr = false;
export const prerender = true;
