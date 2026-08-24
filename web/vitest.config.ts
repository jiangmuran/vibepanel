import { defineConfig } from 'vitest/config'

// Unit tests for logic that does not need a browser. Anything that does — the
// terminal, the layout, WebAuthn — is covered by scripts/render-check.mjs
// against a real one instead, because a jsdom approximation of those would
// pass while the real thing was broken.
export default defineConfig({
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
  },
})
