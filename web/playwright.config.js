import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './tests', workers: 1, retries: 0, reporter: 'list',
  outputDir: '/tmp/october-bus-browser-tests',
  use: { baseURL: 'http://127.0.0.1:4794', headless: true, launchOptions: process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {} },
  webServer: { command: 'node tests/serve.mjs', url: 'http://127.0.0.1:4794/app', reuseExistingServer: false },
});
