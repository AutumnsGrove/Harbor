import { applyD1Migrations, env } from 'cloudflare:test';

// One throwaway database per test file, with the real migrations.
await applyD1Migrations(env.DB, env.TEST_MIGRATIONS);
