import { defineConfig } from '@playwright/test'

// One worker: every spec talks to the same OpenLDAP and Samba AD.
export default defineConfig({
  testDir: 'e2e',
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  timeout: 30_000,
  expect: { timeout: 8_000 },
  globalSetup: './e2e/global-setup.ts',
  globalTeardown: './e2e/global-teardown.ts',
  use: {
    baseURL: 'http://127.0.0.1:18091/ldap/',
    // The token e2e/serve.sh starts the server with.
    extraHTTPHeaders: { Authorization: 'Bearer e2e-token-000000000' },
    channel: 'chrome',
    headless: true,
    viewport: { width: 1400, height: 900 },
    trace: 'retain-on-failure',
  },
  // npm run e2e builds e2e/.tmp/ldaplens; these only start it. Two processes share no state, so
  // each has its own failed sign-in limits: wrong passwords in servermode.spec.ts lock nobody out
  // of the local instance.
  webServer: [
    {
      command: 'sh e2e/serve.sh',
      url: 'http://127.0.0.1:18091/ldap/healthz',
      reuseExistingServer: false,
      // Empty is unset: a variable in the shell running the tests does not reach the server.
      env: { LDAPLENS_NOTICE: '', LDAPLENS_NOTICE_COLOR: '', LDAPLENS_SECURE_COOKIES: '' },
    },
    {
      // Server mode at the root path, without a token: servermode.spec.ts.
      command:
        'e2e/.tmp/ldaplens server --config ../deploy/e2e/connections.server.yaml --listen 127.0.0.1:18092 >e2e/.tmp/server-mode.log 2>&1',
      url: 'http://127.0.0.1:18092/healthz',
      reuseExistingServer: false,
      env: {
        LDAPLENS_AUTH_TOKEN: '',
        LDAPLENS_AUTH_TOKEN_FILE: '',
        LDAPLENS_BASE_PATH: '',
        LDAPLENS_DATA_DIR: '',
        // Markup and a newline: the UI shows both as text.
        LDAPLENS_NOTICE: 'Public demo <b>x</b>\nsecond',
        // Amber: the UI picks black text on it.
        LDAPLENS_NOTICE_COLOR: '#f59e0b',
        LDAPLENS_SECURE_COOKIES: '',
      },
    },
  ],
})
