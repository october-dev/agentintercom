import { test, expect } from '@playwright/test';
import { createHash } from 'node:crypto';

async function fixture(page, signedIn = true, options = {}) {
  const user = { id: 'owner-id', email: 'owner@example.test', aud: 'authenticated', role: 'authenticated', email_confirmed_at: '2026-01-01T00:00:00Z' };
  const events = [];
  let revoked = false;
  const devices = options.devices || [{ id: 'device-1', name: 'My AI app', kind: 'mcp', userId: user.id, expiresAt: '2026-11-24T12:00:00Z' }];
  let keyGeneration = 0;
  const messages = [];
  const tasks = [];
  const channels = [], posts = [], files = [];
  let answered = false;
  if (signedIn) await page.addInitScript(user => {
    const claims = btoa(JSON.stringify({ sub: user.id, aud: 'authenticated', exp: Math.floor(Date.now() / 1000) + 3600 }));
    localStorage.setItem('agentintercom-auth', JSON.stringify({ access_token: 'e30.' + claims + '.fixture', refresh_token: 'fixture-refresh', expires_at: Math.floor(Date.now() / 1000) + 3600, expires_in: 3600, token_type: 'bearer', user }));
  }, user);
  await page.route('https://auth.example.test/**', async route => {
    if (route.request().url().endsWith('/otp?redirect_to=http%3A%2F%2F127.0.0.1%3A4794%2Fapp')) events.push('email-requested');
    await route.fulfill({ json: route.request().url().includes('/user') ? user : {} });
  });
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url()); const path = url.pathname; const method = route.request().method();
    if (method !== 'GET') events.push({ path, method, body: route.request().headers()['content-type'] === 'application/octet-stream' ? { binary: true } : route.request().postDataJSON() });
    let result;
    if (path === '/api/config') result = { authUrl: 'https://auth.example.test', authPublicKey: 'sb_publishable_fixture_key_for_tests', mcpUrl: 'https://bus.example.test/mcp' };
    else if (path === '/api/device/authorizations/ABCD-EFGH') result = method === 'GET' ? { id: 'pair-1', name: '<script>My laptop</script>', userCode: 'ABCD-EFGH' } : { completed: true };
    else if (path === '/api/me') result = user;
    else if (path === '/api/workspaces') result = [{ id: 'workspace-1', name: 'My workspace', role: 'owner', memberLimit: 5 }];
    else if (path.endsWith('/sessions')) result = { sessions: [{ id: 'session-1', displayName: '<img src=x onerror=alert(1)>', deviceId: 'device-1', deviceName: 'Laptop', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' }] };
    else if (path.endsWith('/attachments')) {
      if (method === 'POST') { const data = route.request().postDataBuffer(); const id = 'att_' + String(files.length + 1).padStart(32, '0'); const file = { id, name: url.searchParams.get('name'), mediaType: url.searchParams.get('mediaType'), size: data.length, sha256: createHash('sha256').update(data).digest('hex'), authorKind: 'human', authorId: user.id, uri: 'october-bus://attachments/' + id, createdAt: new Date().toISOString(), data }; files.push(file); result = file; } else result = { attachments: files };
    }
    else if (path.includes('/attachments/')) {
      const id = path.split('/attachments/')[1].split('/')[0]; const file = files.find(f => f.id === id);
      if (!file) return route.fulfill({ status: 404, json: { ok: false, error: { message: 'File was deleted.' } } });
      if (method === 'DELETE') { files.splice(files.indexOf(file), 1); result = { deleted: true }; }
      else return route.fulfill({ contentType: 'application/octet-stream', headers: { 'Content-Disposition': 'attachment; filename="' + file.name + '"', 'X-Content-SHA256': file.sha256 }, body: file.data });
    }
    else if (path.endsWith('/messages')) {
      if (method === 'POST') { const data = route.request().postDataJSON(); messages.unshift({ id: 'message-1', from: user.id, fromKind: 'human', to: data.to, toKind: 'agent', body: data.body, mode: data.mode, state: 'queued', createdAt: new Date().toISOString() }); result = { messageId: 'message-1', state: 'queued' }; } else result = { messages };
    }
    else if (path.endsWith('/channels')) {
      if (method === 'POST') { const item = { ...route.request().postDataJSON(), id: 'channel-' + (channels.length + 1), subscribers: 1 }; channels.push(item); result = item; } else result = channels;
    }
    else if (path.endsWith('/channel-posts')) {
      if (method === 'POST') { const item = { ...route.request().postDataJSON(), id: 'post-' + (posts.length + 1), authorKind: 'human', authorId: user.id, createdAt: new Date().toISOString(), replies: 0 }; posts.unshift(item); result = item; }
      else { const thread = url.searchParams.get('threadId'), search = url.searchParams.get('query'); result = { posts: posts.filter(p => p.channelId === url.searchParams.get('channelId') && (thread ? p.id === thread || p.threadId === thread : search ? p.body.toLowerCase().includes(search.toLowerCase()) : !p.threadId)).map(p => ({ ...p, replies: posts.filter(r => r.threadId === p.id).length })) }; }
    }
    else if (path.endsWith('/tasks')) { if (method === 'POST') { const task = { ...route.request().postDataJSON(), id: 'task-1', status: 'open', ready: true }; tasks.push(task); result = task; } else result = { tasks }; }
    else if (path.endsWith('/questions')) result = { escalations: answered ? [] : [{ id: 'ask-1', agentId: 'session-1', question: 'Which approach?', options: ['First', 'Second'] }] };
    else if (path.endsWith('/questions/ask-1/answer')) { answered = true; result = { answer: route.request().postDataJSON().answer }; }
    else if (path.endsWith('/members')) result = [{ userId: user.id, email: user.email, role: 'owner', joinedAt: '2026-10-24T12:00:00Z' }];
    else if (path.endsWith('/mcp-keys') && method === 'POST') {
      const input = route.request().postDataJSON();
      const connection = { id: input.replaceDeviceId || 'key-1', name: input.name, kind: 'mcp_key', userId: user.id, expiresAt: '2027-01-22T12:00:00Z' };
      const previous = devices.findIndex(device => device.id === connection.id);
      if (previous >= 0) devices[previous] = connection; else devices.push(connection);
      result = { ...connection, token: 'obk_fixture_secret_' + (++keyGeneration) };
    }
    else if (path.endsWith('/devices')) result = devices.map(device => ({ ...device, ...(device.id === 'device-1' && revoked ? { revokedAt: '2026-10-24T12:00:00Z' } : {}) }));
    else if (path.endsWith('/devices/device-1') && method === 'DELETE') { revoked = true; result = { revoked: true }; }
    else if (path.endsWith('/invites')) result = method === 'POST' ? { id: 'invite-1', email: 'teammate@example.test', token: 'secret-invitation', expiresAt: '2026-10-31T12:00:00Z' } : [];
    else if (path === '/api/oauth/requests/request-1') result = method === 'POST' ? { redirectUrl: 'http://127.0.0.1:4794/callback?code=fixture&state=state' } : { id: 'request-1', clientName: 'Connected app', redirectUri: 'http://127.0.0.1:4794/callback' };
    else { await route.fulfill({ status: 404, json: { error: { message: `Unexpected fixture route ${path}` } } }); return; }
    await route.fulfill({ json: { ok: true, result } });
  });
  return events;
}

test('signed-in workspace shows honest presence and safe names, and revokes devices', async ({ page }) => {
  const events = await fixture(page);
  await page.goto('/app');
  await expect(page.getByRole('heading', { name: 'Sessions', exact: true })).toBeVisible();
  await expect(page.getByText('<img src=x onerror=alert(1)>', { exact: true })).toBeVisible();
  await expect(page.locator('img')).toHaveCount(0);
  await expect(page.getByText('Connected', { exact: true })).toBeVisible();
  await expect(page.getByText(/may need you to resume/)).toBeVisible();
  await page.getByRole('button', { name: 'Members & devices', exact: true }).click();
  await page.getByRole('button', { name: 'Revoke', exact: true }).click();
  expect(events).toHaveLength(0);
  await page.getByRole('button', { name: 'Revoke connection', exact: true }).click();
  await expect(page.getByText(/MCP app · Revoked/)).toBeVisible();
  expect(events.filter(e => e.method === 'DELETE')).toHaveLength(1);
});

test('OAuth requires explicit workspace consent and preserves the app callback', async ({ page }) => {
  const events = await fixture(page);
  await page.route('**/callback?*', route => route.fulfill({ contentType: 'text/html', body: '<p>Connection completed</p>' }));
  await page.goto('/app?authorization_id=request-1');
  await expect(page.getByRole('heading', { name: 'Connected app' })).toBeVisible();
  await expect(page.getByText(/October has not verified/)).toBeVisible();
  expect(events).toHaveLength(0);
  await page.getByRole('button', { name: 'Allow connection', exact: true }).click();
  await expect(page).toHaveURL(/\/callback\?code=fixture&state=state/);
  expect(events).toEqual([{ path: '/api/oauth/requests/request-1', method: 'POST', body: { approve: true, workspaceId: 'workspace-1' } }]);
});

test('mobile onboarding and teammate invitation stay usable without overflow', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const events = await fixture(page);
  await page.goto('/app');
  await page.getByRole('button', { name: 'Connect an app', exact: true }).last().click();
  await expect(page.getByText('https://bus.example.test/mcp', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Members & devices', exact: true }).click();
  await page.getByLabel('Teammate’s email').fill('teammate@example.test');
  await page.getByRole('button', { name: 'Create invitation', exact: true }).click();
  await expect(page.getByText('http://127.0.0.1:4794/app#invite=secret-invitation', { exact: true })).toBeVisible();
  expect(events.at(-1).body).toEqual({ email: 'teammate@example.test', role: 'member' });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
  await page.screenshot({ path: '/tmp/agentintercom-workspace-mobile.png', fullPage: true });
});

test('signed-out page presents account login without sending an email on load', async ({ page }) => {
  const events = await fixture(page, false);
  await page.goto('/app');
  await expect(page.getByRole('heading', { name: 'Sign in to October' })).toBeVisible();
  await expect(page.getByLabel('Email address', { exact: true }).first()).toBeVisible();
  expect(events).toHaveLength(0);
  await page.screenshot({ path: '/tmp/agentintercom-signin.png', fullPage: true });
});


test('human conversation, shared task and question flows preserve human intent', async ({ page }) => {
  const events = await fixture(page);
  await page.goto('/app');
  await page.getByRole('button', { name: 'Open conversation', exact: true }).click();
  await page.getByLabel('Your message').fill('Please review this change.');
  await page.getByRole('button', { name: 'Send request', exact: true }).click();
  await expect(page.getByText('Please review this change.', { exact: true })).toBeVisible();
  await expect(page.getByText('Waiting for delivery', { exact: true })).toBeVisible();
  expect(events.at(-1).body.mode).toBe('request');
  expect(events.at(-1).body.idempotencyKey).toBeTruthy();
  await page.getByRole('button', { name: 'Tasks', exact: true }).click();
  await page.getByLabel('Task title').fill('Review implementation');
  await page.getByLabel('Description', { exact: true }).fill('Check the important boundaries.');
  await page.getByRole('button', { name: 'Create task', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Review implementation' })).toBeVisible();
  await page.getByRole('button', { name: 'Needs you', exact: true }).click();
  await page.getByRole('button', { name: 'Second', exact: true }).click();
  expect(events.filter(e => e.path.includes('/answer'))).toHaveLength(0);
  await expect(page.getByLabel('Your answer')).toHaveValue('Second');
  await page.getByRole('button', { name: 'Send answer', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Nothing waiting on you.' })).toBeVisible();
  expect(events.at(-1).body).toEqual({ answer: 'Second' });
});

test('device pairing waits for explicit code confirmation and workspace consent', async ({ page }) => {
  const events = await fixture(page);
  await page.goto('/app#device=ABCD-EFGH');
  await expect(page.getByRole('heading', { name: '<script>My laptop</script>', exact: true })).toBeVisible();
  await expect(page.getByText('ABCD-EFGH', { exact: true })).toBeVisible();
  expect(new URL(page.url()).hash).toBe('');
  expect(events).toHaveLength(0);
  await page.getByRole('button', { name: 'Connect this device', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Your device can connect.' })).toBeVisible();
  expect(events).toEqual([{ path: '/api/device/authorizations/ABCD-EFGH', method: 'POST', body: { approve: true, workspaceId: 'workspace-1' } }]);
  await page.getByRole('button', { name: 'Open workspace', exact: true }).click();
  await page.getByRole('navigation', { name: 'Workspace' }).getByRole('button', { name: 'Connect an app', exact: true }).click();
  await expect(page.getByText(/agentintercom hosted login --server/)).toBeVisible();
});

test('device recovery is an explicit choice among only the account’s machines', async ({ page }) => {
  const events = await fixture(page, true, { devices: [
    { id: 'mine', userId: 'owner-id', name: 'Old laptop', kind: 'device', revokedAt: '2026-10-24T12:00:00Z' },
    { id: 'other', userId: 'another-person', name: 'Other laptop', kind: 'device' },
    { id: 'app', userId: 'owner-id', name: 'AI app', kind: 'mcp' },
  ] });
  await page.goto('/app#device=ABCD-EFGH');
  const choice = page.getByRole('combobox', { name: 'Device connection', exact: true });
  await expect(choice).toHaveValue('');
  await expect(choice.locator('option')).toHaveCount(2);
  await choice.selectOption('mine');
  await expect(page.getByText(/Replacing a connection signs out/)).toBeVisible();
  expect(events).toHaveLength(0);
  await page.getByRole('button', { name: 'Connect this device', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Your device can connect.' })).toBeVisible();
  expect(events.at(-1).body).toEqual({ approve: true, workspaceId: 'workspace-1', replaceDeviceId: 'mine' });
});

test('OAuth reconnection keeps an inbox only after choosing it at consent', async ({ page }) => {
  const events = await fixture(page);
  await page.route('**/callback?*', route => route.fulfill({ contentType: 'text/html', body: '<p>Connection completed</p>' }));
  await page.goto('/app?authorization_id=request-1');
  await expect(page.getByRole('combobox', { name: 'App connection', exact: true })).toHaveValue('');
  await page.getByRole('combobox', { name: 'App connection', exact: true }).selectOption('device-1');
  await expect(page.getByText(/old app credentials will stop working/)).toBeVisible();
  expect(events).toHaveLength(0);
  await page.getByRole('button', { name: 'Allow connection', exact: true }).click();
  await expect(page.getByText('Connection completed')).toBeVisible();
  expect(events.at(-1).body).toEqual({ approve: true, workspaceId: 'workspace-1', replaceDeviceId: 'device-1' });
});

test('manual MCP key is shown once and replacement requires a deliberate action', async ({ page }) => {
  const events = await fixture(page);
  await page.goto('/app');
  await page.getByRole('navigation', { name: 'Workspace' }).getByRole('button', { name: 'Connect an app', exact: true }).click();
  await page.getByLabel('App connection name').fill('Automation app');
  await page.getByRole('button', { name: 'Create MCP key', exact: true }).click();
  await expect(page.getByText('obk_fixture_secret_1', { exact: true })).toBeVisible();
  await page.getByRole('dialog').getByRole('button', { name: 'Close', exact: true }).click();
  await expect(page.getByText('obk_fixture_secret_1', { exact: true })).toHaveCount(0);
  expect(await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }))).not.toContain('obk_fixture');
  await page.getByRole('button', { name: 'Members & devices', exact: true }).click();
  await page.getByRole('button', { name: 'Replace key', exact: true }).click();
  expect(events.filter(event => event.path?.endsWith('/mcp-keys'))).toHaveLength(1);
  await expect(page.getByRole('dialog').getByText(/old key for Automation app will stop/)).toBeVisible();
  await page.getByRole('button', { name: 'Create replacement key', exact: true }).click();
  await expect(page.getByText('obk_fixture_secret_2', { exact: true })).toBeVisible();
  expect(events.at(-1).body).toEqual({ name: 'Automation app', replaceDeviceId: 'key-1' });
});

test('channel creation, literal posts, thread replies and search share one conversation', async ({ page }) => {
  const events = await fixture(page);
  await page.goto('/app');
  await page.getByRole('navigation', { name: 'Workspace' }).getByRole('button', { name: 'Channels', exact: true }).click();
  await page.getByRole('button', { name: 'Create a channel', exact: true }).click();
  await page.getByLabel('Channel name', { exact: true }).fill('release');
  await page.getByLabel('Description', { exact: true }).fill('Release discussion');
  await page.getByRole('button', { name: 'Create channel', exact: true }).click();
  await page.getByLabel('Your post', { exact: true }).fill('<script>alert(1)</script> Release is ready');
  await page.getByRole('button', { name: 'Post to channel', exact: true }).click();
  await expect(page.getByText('<script>alert(1)</script> Release is ready', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Open thread', exact: true }).click();
  await page.getByLabel('Your reply', { exact: true }).fill('Reviewed the release notes');
  await page.getByRole('button', { name: 'Reply in thread', exact: true }).click();
  await expect(page.getByText('Reviewed the release notes', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Back to channel', exact: true }).click();
  await expect(page.getByRole('button', { name: '1 reply', exact: true })).toBeVisible();
  await page.getByLabel('Search channel', { exact: true }).fill('Reviewed');
  await page.getByRole('button', { name: 'Search posts', exact: true }).click();
  await expect(page.getByText('Reviewed the release notes', { exact: true })).toBeVisible();
  expect(events.filter(e => e.path?.endsWith('/channel-posts'))).toHaveLength(2);
  expect(events.filter(e => e.path?.endsWith('/channel-posts'))[1].body.threadId).toBe('post-1');
});

test('files upload, download, attach to conversations and require confirmation to delete', async ({ page }) => {
  const events = await fixture(page);
  await page.goto('/app');
  await page.getByRole('navigation', { name: 'Workspace' }).getByRole('button', { name: 'Files', exact: true }).click();
  await page.getByLabel('File to upload').setInputFiles({ name: 'report.txt', mimeType: 'text/plain', buffer: Buffer.from('Private report') });
  await page.getByRole('button', { name: 'Upload file', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'report.txt', exact: true })).toBeVisible();
  const downloadEvent = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download', exact: true }).click();
  expect((await downloadEvent).suggestedFilename()).toBe('report.txt');
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  await page.getByLabel('Your message', { exact: true }).fill('Please review this.');
  await page.getByText('Attach a file', { exact: true }).first().click();
  await page.getByLabel('Attach a file', { exact: true }).setInputFiles({ name: 'review.txt', mimeType: 'text/plain', buffer: Buffer.from('Review content') });
  await page.getByRole('button', { name: 'Upload and attach', exact: true }).click();
  await expect(page.getByLabel('Your message', { exact: true })).toHaveValue(/Please review this\.[\s\S]*october-bus:\/\/attachments\/att_/);
  await page.getByRole('button', { name: 'Send request', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Download shared file', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Files', exact: true }).click();
  await page.getByRole('article').filter({ has: page.getByRole('heading', { name: 'report.txt', exact: true }) }).getByRole('button', { name: 'Delete file', exact: true }).click();
  expect(events.filter(e => e.method === 'DELETE')).toHaveLength(0);
  await page.getByRole('button', { name: 'Delete permanently', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'report.txt', exact: true })).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'review.txt', exact: true })).toBeVisible();
});

test('billing shows actual entitlement and owner controls, never trusts a checkout return', async ({ page }) => {
  await fixture(page);
  let refreshed = false;
  const summary = { configured: true, live: false, canManage: true, active: true, status: 'none', trialEndsAt: new Date(Date.now() + 86400000).toISOString(), paidUntil: '', pendingCheckout: true, pendingInterval: 'month', monthlyPriceCents: 1900, annualPriceCents: 19000, currency: 'usd', deliveries: 23, deliveryLimit: 2000, month: '2026-10', fileBytes: 0, fileStorageLimit: 256 * 1024 ** 2, uploadedBytes: 0, downloadedBytes: 0, fileTransferLimit: 1024 ** 3, members: 1, memberLimit: 5, sessions: 2, sessionLimit: 50 };
  await page.route('**/api/workspaces/workspace-1/billing**', async route => {
    const url = new URL(route.request().url());
    if (url.pathname.endsWith('/refresh')) { refreshed = true; Object.assign(summary, { status: 'active', paidUntil: new Date(Date.now() + 30 * 86400000).toISOString(), pendingCheckout: false, deliveryLimit: 100000 }); }
    await route.fulfill({ json: { ok: true, result: summary } });
  });
  await page.goto('/app?billing=success&workspace=workspace-1');
  await expect(page.getByRole('heading', { name: 'Workspace trial', exact: true })).toBeVisible();
  await expect(page.getByText(/returning from checkout does not confirm a payment/)).toBeVisible();
  await expect(page.getByText(/Test billing/)).toBeVisible();
  await expect(page.getByRole('button', { name: '$19 / month', exact: true })).toBeVisible();
  await expect(page.getByRole('progressbar', { name: 'New deliveries', exact: true })).toHaveAttribute('value', '23');
  await page.getByRole('button', { name: 'Check payment status', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Paid workspace', exact: true })).toBeVisible();
  expect(refreshed).toBe(true);
  await expect(page.getByRole('button', { name: 'Payment settings & cancellation', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: '$19 / month', exact: true })).toHaveCount(0);
});

test('data controls confirm pauses and cleanup, then download a portable archive', async ({ page }) => {
  await fixture(page);
  let paused = false;
  const cleanups = [];
  await page.route('**/api/workspaces', route => route.fulfill({ json: { ok: true, result: [{ id: 'workspace-1', name: 'My workspace', role: 'owner', paused }] } }));
  await page.route(/\/api\/workspaces\/workspace-1\/(storage|pause|prune|export)$/, async route => {
    const path = new URL(route.request().url()).pathname;
    let result = {};
    if (path.endsWith('/storage')) result = { records: [{ recordType: 'message', state: 'acknowledged', count: 2 }] };
    if (path.endsWith('/pause')) paused = route.request().postDataJSON().paused;
    if (path.endsWith('/prune')) { cleanups.push(route.request().postDataJSON()); result = { records: { messages: 2, tasks: 0, events: 4 }, dryRun: !cleanups.at(-1).execute }; }
    if (path.endsWith('/export')) return route.fulfill({ contentType: 'application/json', headers: { 'Content-Disposition': 'attachment; filename="agentintercom-workspace.json"' }, body: JSON.stringify({ format: 'october-bus.scope', version: 3 }) });
    await route.fulfill({ json: { ok: true, result } });
  });
  await page.goto('/app');
  await page.getByRole('button', { name: 'Data & recovery', exact: true }).click();
  await page.getByRole('button', { name: 'Pause sessions', exact: true }).click();
  expect(paused).toBe(false);
  await page.getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Sessions paused', exact: true })).toBeVisible();
  const download = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download workspace archive', exact: true }).click();
  expect((await download).suggestedFilename()).toBe('agentintercom-workspace.json');
  await page.getByRole('button', { name: 'Preview cleanup', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Delete eligible history?', exact: true })).toBeVisible();
  expect(cleanups.map(c => c.execute)).toEqual([false]);
  await page.getByRole('button', { name: 'Delete eligible history', exact: true }).click();
  await expect(page.getByText('Completed history pruned.', { exact: true })).toBeVisible();
  expect(cleanups.map(c => c.execute)).toEqual([false, true]);
});

test('conversations reach sessions beyond the first page and isolate workspace switches', async ({ page }) => {
  await fixture(page);
  const sessionRequests = [];
  let delayedResponses = 0, delayFirst = 0;
  const page1 = [
    { id: 'session-001', displayName: 'Agent Dup', deviceId: 'device-1', deviceName: 'Laptop A', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' },
    { id: 'session-002', displayName: 'Agent Dup', deviceId: 'device-2', deviceName: 'Laptop B', reachable: false, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' },
  ];
  for (let i = 3; i <= 100; i++) page1.push({ id: `session-${String(i).padStart(3, '0')}`, displayName: `Agent ${i}`, deviceId: 'device-1', deviceName: 'Laptop A', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' });
  const page2 = [{ id: 'session-101', displayName: 'Late Agent', deviceId: 'device-3', deviceName: 'Old laptop', reachable: false, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' }];
  await page.route('**/api/workspaces', route => route.fulfill({ json: { ok: true, result: [
    { id: 'workspace-1', name: 'My workspace', role: 'owner', memberLimit: 5 },
    { id: 'workspace-2', name: 'Second workspace', role: 'owner', memberLimit: 5 },
  ] } }));
  await page.route('**/api/workspaces/workspace-1/sessions*', async route => {
    const url = new URL(route.request().url());
    sessionRequests.push(url.pathname + url.search);
    if (url.searchParams.get('after')) {
      return route.fulfill({ json: { ok: true, result: { sessions: page2 } } });
    }
    if (delayFirst) { await new Promise(r => setTimeout(r, delayFirst)); delayedResponses++; }
    await route.fulfill({ json: { ok: true, result: { sessions: page1, nextCursor: 'session-100' } } });
  });
  await page.route('**/api/workspaces/workspace-2/sessions*', async route => {
    sessionRequests.push(new URL(route.request().url()).pathname);
    await route.fulfill({ json: { ok: true, result: { sessions: [{ id: 'ws2-session', displayName: 'Second Workspace Agent', deviceId: 'device-9', deviceName: 'Tablet', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' }] } } });
  });
  const messageQueries = [];
  await page.route('**/api/workspaces/workspace-1/messages*', async route => {
    messageQueries.push(new URL(route.request().url()).search);
    await route.fallback();
  });
  await page.goto('/app');
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  const sendTo = page.locator('select[name="to"]');
  await expect(sendTo.locator('option')).toHaveCount(101);
  await expect(sendTo.locator('option[value="session-101"]')).toHaveText('Late Agent · offline');
  await expect(sendTo.locator('option[value="session-001"]')).toHaveText('Agent Dup · Laptop A');
  await expect(sendTo.locator('option[value="session-002"]')).toHaveText('Agent Dup · Laptop B · offline');
  expect(sessionRequests.some(u => u.includes('after=session-100'))).toBe(true);
  const filter = page.locator('select[aria-label="Filter by session"]');
  await expect(filter.locator('option[value="session-101"]')).toHaveText('Late Agent');
  await filter.selectOption('session-101');
  await expect.poll(() => messageQueries.some(q => q.includes('agentId=session-101')), { timeout: 5000 }).toBe(true);
  await expect(page.locator('select[aria-label="Filter by session"]')).toHaveValue('session-101');
  delayFirst = 1200;
  const continuationsBefore = sessionRequests.filter(u => u.includes('after=')).length;
  await page.getByRole('button', { name: 'Refresh conversation', exact: true }).click();
  await page.locator('select[aria-label="Workspace"]').selectOption('workspace-2');
  await expect(page.locator('select[name="to"]').locator('option')).toHaveCount(1);
  await expect(page.locator('select[name="to"]').locator('option[value="ws2-session"]')).toHaveText('Second Workspace Agent');
  await expect.poll(() => delayedResponses, { timeout: 5000 }).toBe(1);
  expect(sessionRequests.filter(u => u.includes('after=')).length).toBe(continuationsBefore);
  await expect(page.locator('select[name="to"]').locator('option')).toHaveCount(1);
  await expect(page.locator('select[aria-label="Filter by session"]').locator('option')).toHaveCount(2);
});

test('conversation drafts survive navigation and refresh, stay in their workspace, and clear on send', async ({ page }) => {
  await fixture(page);
  await page.route('**/api/workspaces', route => route.fulfill({ json: { ok: true, result: [
    { id: 'workspace-1', name: 'My workspace', role: 'owner', memberLimit: 5 },
    { id: 'workspace-2', name: 'Second workspace', role: 'owner', memberLimit: 5 },
  ] } }));
  await page.goto('/app');
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  const composer = page.getByLabel('Your message', { exact: true });
  await composer.fill('Please review this draft.');
  await page.getByText('Attach a file', { exact: true }).first().click();
  await page.getByLabel('Attach a file', { exact: true }).setInputFiles({ name: 'review.txt', mimeType: 'text/plain', buffer: Buffer.from('Review content') });
  await page.getByRole('button', { name: 'Upload and attach', exact: true }).click();
  await expect(composer).toHaveValue(/Please review this draft\.[\s\S]*october-bus:\/\/attachments\/att_/);
  expect(await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }))).not.toContain('Please review this draft.');
  await page.getByRole('button', { name: 'Tasks', exact: true }).click();
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  await expect(composer).toHaveValue(/october-bus:\/\/attachments\/att_/);
  await page.getByRole('button', { name: 'Refresh conversation', exact: true }).click();
  await expect(composer).toHaveValue(/october-bus:\/\/attachments\/att_/);
  await page.locator('select[aria-label="Workspace"]').selectOption('workspace-2');
  await expect(composer).toHaveValue('');
  await page.locator('select[aria-label="Workspace"]').selectOption('workspace-1');
  await expect(composer).toHaveValue(/Please review this draft\./);
  await page.getByRole('button', { name: 'Send request', exact: true }).click();
  await expect(page.getByText(/Please review this draft\./)).toBeVisible();
  await expect(composer).toHaveValue('');
});

test('failed sends keep the draft and retry with the same idempotency key', async ({ page }) => {
  await fixture(page);
  const postedKeys = [];
  await page.route('**/api/workspaces/workspace-1/messages*', async route => {
    if (route.request().method() === 'POST' && postedKeys.length === 0) {
      postedKeys.push(route.request().postDataJSON().idempotencyKey);
      return route.fulfill({ status: 500, json: { ok: false, error: { message: 'Delivery uncertain. Please retry.' } } });
    }
    if (route.request().method() === 'POST') postedKeys.push(route.request().postDataJSON().idempotencyKey);
    await route.fallback();
  });
  await page.goto('/app');
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  await page.getByLabel('Your message', { exact: true }).fill('Retry this request.');
  await page.getByRole('button', { name: 'Send request', exact: true }).click();
  await expect(page.getByText('Delivery uncertain. Please retry.', { exact: true })).toBeVisible();
  await expect(page.getByLabel('Your message', { exact: true })).toHaveValue('Retry this request.');
  await page.getByRole('button', { name: 'Send request', exact: true }).click();
  await expect(page.getByText('Retry this request.', { exact: true })).toBeVisible();
  expect(postedKeys).toHaveLength(2);
  expect(postedKeys[0]).toBeTruthy();
  expect(postedKeys[1]).toBe(postedKeys[0]);
  await expect(page.getByLabel('Your message', { exact: true })).toHaveValue('');
});

test('channel and thread drafts stay independent and clear only on send', async ({ page }) => {
  await fixture(page);
  await page.goto('/app');
  await page.getByRole('navigation', { name: 'Workspace' }).getByRole('button', { name: 'Channels', exact: true }).click();
  await page.getByRole('button', { name: 'Create a channel', exact: true }).click();
  await page.getByLabel('Channel name', { exact: true }).fill('drafts');
  await page.getByLabel('Description', { exact: true }).fill('Draft discussion');
  await page.getByRole('button', { name: 'Create channel', exact: true }).click();
  await page.getByLabel('Your post', { exact: true }).fill('First post body');
  await page.getByRole('button', { name: 'Post to channel', exact: true }).click();
  await expect(page.getByText('First post body', { exact: true })).toBeVisible();
  await page.getByLabel('Your post', { exact: true }).fill('Second post body');
  await page.getByRole('button', { name: 'Post to channel', exact: true }).click();
  await expect(page.getByText('Second post body', { exact: true })).toBeVisible();
  await expect(page.getByLabel('Your post', { exact: true })).toHaveValue('');
  await page.getByLabel('Your post', { exact: true }).fill('Unsent channel draft');
  await page.getByRole('button', { name: 'Open thread', exact: true }).first().click();
  await expect(page.getByLabel('Your reply', { exact: true })).toHaveValue('');
  await page.getByLabel('Your reply', { exact: true }).fill('Reply draft one');
  await page.getByRole('button', { name: 'Back to channel', exact: true }).click();
  await expect(page.getByLabel('Your post', { exact: true })).toHaveValue('Unsent channel draft');
  await page.getByRole('button', { name: 'Open thread', exact: true }).nth(1).click();
  await expect(page.getByLabel('Your reply', { exact: true })).toHaveValue('');
  await page.getByLabel('Your reply', { exact: true }).fill('Reply draft two');
  await page.getByRole('button', { name: 'Back to channel', exact: true }).click();
  await page.getByRole('button', { name: 'Open thread', exact: true }).first().click();
  await expect(page.getByLabel('Your reply', { exact: true })).toHaveValue('Reply draft one');
  await page.getByRole('button', { name: 'Reply in thread', exact: true }).click();
  await expect(page.getByText('Reply draft one', { exact: true })).toBeVisible();
  await expect(page.getByLabel('Your reply', { exact: true })).toHaveValue('');
  await page.getByRole('button', { name: 'Back to channel', exact: true }).click();
  await expect(page.getByLabel('Your post', { exact: true })).toHaveValue('Unsent channel draft');
});

test('drafts never appear under a different signed-in account', async ({ page }) => {
  await fixture(page);
  await page.goto('/app');
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  await page.getByLabel('Your message', { exact: true }).fill('Owner private draft');
  const intruder = { id: 'intruder-id', email: 'intruder@example.test', aud: 'authenticated', role: 'authenticated', email_confirmed_at: '2026-01-01T00:00:00Z' };
  const claims = Buffer.from(JSON.stringify({ sub: intruder.id, aud: 'authenticated', exp: Math.floor(Date.now() / 1000) + 3600 })).toString('base64');
  await page.route('**/auth/v1/token*', route => route.fulfill({ json: {
    access_token: 'e30.' + claims + '.fixture', refresh_token: 'fixture-refresh-intruder',
    expires_in: 3600, expires_at: Math.floor(Date.now() / 1000) + 3600, token_type: 'bearer', user: intruder,
  } }));
  await page.route('**/api/me', route => route.fulfill({ json: { ok: true, result: intruder } }));
  await page.getByRole('button', { name: 'Sign out', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Sign in to October' })).toBeVisible();
  await page.getByText('Use an existing password', { exact: true }).click();
  await page.getByLabel('Email address', { exact: true }).last().fill('intruder@example.test');
  await page.getByLabel('Password', { exact: true }).fill('not-a-real-password');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByText('intruder@example.test', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  await expect(page.getByLabel('Your message', { exact: true })).toHaveValue('');
});

test('workspace deletion requires its name and recovers a failed cancellation', async ({ page }) => {
  await fixture(page);
  let deleting = false, deleted = false;
  const attempts = [];
  await page.route('**/api/workspaces', route => route.fulfill({ json: { ok: true, result: deleted ? [] : [{ id: 'workspace-1', name: 'My workspace', role: 'owner', paused: deleting, deleting }] } }));
  await page.route('**/api/workspaces/workspace-1/storage', route => route.fulfill({ json: { ok: true, result: { records: [] } } }));
  await page.route('**/api/workspaces/workspace-1', async route => {
    attempts.push(route.request().postDataJSON()); deleting = true;
    if (attempts.length === 1) return route.fulfill({ status: 503, json: { error: { message: 'Cancellation could not be confirmed. Workspace deletion remains pending and will retry' } } });
    deleted = true; await route.fulfill({ json: { ok: true, result: { deleted: true } } });
  });
  await page.goto('/app');
  await page.getByRole('button', { name: 'Data & recovery', exact: true }).click();
  await page.getByRole('button', { name: 'Delete workspace', exact: true }).click();
  expect(attempts).toHaveLength(0);
  await page.getByLabel('Type the workspace name to confirm').fill('Wrong workspace');
  await page.getByRole('button', { name: 'Delete permanently', exact: true }).click();
  await expect(page.getByText('The workspace name does not match.', { exact: true })).toBeVisible();
  expect(attempts).toHaveLength(0);
  await page.getByLabel('Type the workspace name to confirm').fill('My workspace');
  await page.getByRole('button', { name: 'Delete permanently', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Deletion in progress', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Resume sessions', exact: true })).toHaveCount(0);
  await page.getByRole('button', { name: 'Retry workspace deletion', exact: true }).click();
  await page.getByLabel('Type the workspace name to confirm').fill('My workspace');
  await page.getByRole('button', { name: 'Delete permanently', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Make room for your team.', exact: true })).toBeVisible();
  expect(attempts).toEqual([{ confirmName: 'My workspace' }, { confirmName: 'My workspace' }]);
});

test('dialogs are found by their accessible name and keyboard focus stays predictable', async ({ page }) => {
  const events = await fixture(page);
  await page.goto('/app');
  const newWorkspaceTrigger = page.getByRole('button', { name: '+ New workspace', exact: true });
  await newWorkspaceTrigger.focus();
  await page.keyboard.press('Enter');
  const newWorkspace = page.getByRole('dialog', { name: 'New workspace', exact: true });
  await expect(newWorkspace).toBeVisible();
  await expect(page.getByLabel('Workspace name', { exact: true })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(page.getByRole('button', { name: 'Create workspace', exact: true })).toBeFocused();
  expect(events).toHaveLength(0);
  await page.keyboard.press('Escape');
  await expect(newWorkspace).toHaveCount(0);
  await expect(newWorkspaceTrigger).toBeFocused();
  await newWorkspaceTrigger.press('Enter');
  await expect(page.getByRole('dialog', { name: 'New workspace', exact: true })).toBeVisible();
  await page.getByRole('dialog', { name: 'New workspace', exact: true }).getByRole('button', { name: 'Close', exact: true }).press('Enter');
  await expect(page.getByRole('dialog', { name: 'New workspace' })).toHaveCount(0);
  await expect(newWorkspaceTrigger).toBeFocused();
  expect(events).toHaveLength(0);
});

test('confirmation dialogs are named, keyboard-operable and still guard destructive actions', async ({ page }) => {
  const events = await fixture(page);
  await page.goto('/app');
  await page.getByRole('button', { name: 'Members & devices', exact: true }).click();
  await page.getByRole('button', { name: 'Revoke', exact: true }).focus();
  await page.keyboard.press('Enter');
  const confirmation = page.getByRole('dialog', { name: 'Revoke connection?', exact: true });
  await expect(confirmation).toBeVisible();
  await expect(confirmation.getByText('My AI app will stop receiving access to this workspace.', { exact: true })).toBeVisible();
  expect(events).toHaveLength(0);
  await page.keyboard.press('Escape');
  await expect(confirmation).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Revoke', exact: true })).toBeFocused();
  expect(events).toHaveLength(0);
  await page.getByRole('button', { name: 'Revoke', exact: true }).press('Enter');
  await page.getByRole('dialog', { name: 'Revoke connection?', exact: true }).getByRole('button', { name: 'Revoke connection', exact: true }).press('Enter');
  await expect(page.getByText(/MCP app · Revoked/)).toBeVisible();
  expect(events.filter(event => event.method === 'DELETE')).toHaveLength(1);
});

test('duplicate session names always get distinct labels', async ({ page }) => {
  await fixture(page);
  const sessions = [
    { id: 'dup-1', displayName: 'Claude', deviceId: 'device-1', deviceName: 'Claude', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' },
    { id: 'dup-2', displayName: 'Claude', deviceId: 'device-2', deviceName: 'Claude', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' },
    { id: 'solo-1', displayName: 'Codex', deviceId: 'device-3', deviceName: 'Codex', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' },
    { id: 'laptop-1', displayName: 'Claude', deviceId: 'device-4', deviceName: 'Laptop', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' },
  ];
  await page.route('**/api/workspaces/workspace-1/sessions*', route => route.fulfill({ json: { ok: true, result: { sessions } } }));
  await page.goto('/app');
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  const sendTo = page.locator('select[name="to"]');
  await expect(sendTo.locator('option')).toHaveCount(4);
  await expect(sendTo.locator('option[value="dup-1"]')).toHaveText('Claude · dup-1');
  await expect(sendTo.locator('option[value="dup-2"]')).toHaveText('Claude · dup-2');
  await expect(sendTo.locator('option[value="solo-1"]')).toHaveText('Codex');
  await expect(sendTo.locator('option[value="laptop-1"]')).toHaveText('Claude · Laptop');
  const texts = await sendTo.locator('option').allTextContents();
  expect(new Set(texts).size).toBe(4);
});

test('conversations follow the session directory past fifty pages', async ({ page }) => {
  await fixture(page);
  const total = 55, perPage = 2;
  await page.route('**/api/workspaces/workspace-1/sessions*', async route => {
    const after = new URL(route.request().url()).searchParams.get('after');
    const index = after ? parseInt(after.slice(1), 10) : 0;
    const sessions = [];
    for (let i = 0; i < perPage; i++) {
      const n = index * perPage + i + 1;
      sessions.push({ id: `paged-${n}`, displayName: `Paged ${n}`, deviceId: 'device-1', deviceName: 'Laptop', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' });
    }
    const result = { sessions };
    if (index + 1 < total) result.nextCursor = 'p' + (index + 1);
    await route.fulfill({ json: { ok: true, result } });
  });
  await page.goto('/app');
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  const sendTo = page.locator('select[name="to"]');
  await expect(sendTo.locator('option')).toHaveCount(total * perPage);
  await expect(sendTo.locator('option[value="paged-110"]')).toHaveText('Paged 110');
  await expect(page.locator('select[aria-label="Filter by session"]').locator('option[value="paged-110"]')).toHaveText('Paged 110');
});

test('a picked recipient survives navigation but yields to an explicit conversation choice', async ({ page }) => {
  await fixture(page);
  const sessions = [
    { id: 'session-alpha', displayName: 'Agent Alpha', deviceId: 'device-1', deviceName: 'Laptop', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' },
    { id: 'session-beta', displayName: 'Agent Beta', deviceId: 'device-2', deviceName: 'Desktop', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' },
  ];
  await page.route('**/api/workspaces/workspace-1/sessions*', route => route.fulfill({ json: { ok: true, result: { sessions } } }));
  await page.goto('/app');
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  const sendTo = page.locator('select[name="to"]');
  await sendTo.selectOption('session-beta');
  await page.getByRole('button', { name: 'Tasks', exact: true }).click();
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  await expect(sendTo).toHaveValue('session-beta');
  const composer = page.getByLabel('Your message', { exact: true });
  await composer.fill('x');
  await composer.fill('');
  await page.getByRole('button', { name: 'Sessions', exact: true }).click();
  await page.getByRole('article').filter({ hasText: 'Agent Alpha' }).getByRole('button', { name: 'Open conversation', exact: true }).click();
  await expect(page.locator('select[name="to"]')).toHaveValue('session-alpha');
});

test('a late upload never writes into another workspace draft', async ({ page }) => {
  await fixture(page);
  await page.route('**/api/workspaces', route => route.fulfill({ json: { ok: true, result: [
    { id: 'workspace-1', name: 'My workspace', role: 'owner', memberLimit: 5 },
    { id: 'workspace-2', name: 'Second workspace', role: 'owner', memberLimit: 5 },
  ] } }));
  let release;
  const held = new Promise(r => { release = r; });
  let heldCount = 0;
  await page.route('**/api/workspaces/workspace-1/attachments*', async route => {
    if (route.request().method() === 'POST') { heldCount++; await held; }
    await route.fallback();
  });
  await page.goto('/app');
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  await page.getByLabel('Your message', { exact: true }).fill('Workspace one text');
  await page.getByText('Attach a file', { exact: true }).first().click();
  await page.getByLabel('Attach a file', { exact: true }).setInputFiles({ name: 'late.txt', mimeType: 'text/plain', buffer: Buffer.from('late bytes') });
  await page.getByRole('button', { name: 'Upload and attach', exact: true }).click();
  await expect.poll(() => heldCount, { timeout: 5000 }).toBe(1);
  await page.locator('select[aria-label="Workspace"]').selectOption('workspace-2');
  const composer = page.getByLabel('Your message', { exact: true });
  await expect(composer).toHaveValue('');
  await composer.fill('Workspace two text');
  release();
  await expect(page.getByText('File uploaded. Copy its reference from Files to share it.', { exact: true })).toBeVisible();
  await expect(composer).toHaveValue('Workspace two text');
  await page.locator('select[aria-label="Workspace"]').selectOption('workspace-1');
  await expect(page.getByLabel('Your message', { exact: true })).toHaveValue('Workspace one text');
});

test('text typed while a send is pending survives the send', async ({ page }) => {
  const events = await fixture(page);
  let release;
  const held = new Promise(r => { release = r; });
  let posts = 0;
  await page.route('**/api/workspaces/workspace-1/messages*', async route => {
    if (route.request().method() === 'POST') { posts++; await held; }
    await route.fallback();
  });
  await page.goto('/app');
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  const composer = page.getByLabel('Your message', { exact: true });
  await composer.fill('First text');
  await page.getByRole('button', { name: 'Send request', exact: true }).click();
  await expect.poll(() => posts, { timeout: 5000 }).toBe(1);
  await composer.fill('First text plus more');
  release();
  await expect(page.getByText('First text', { exact: true })).toBeVisible();
  await expect(page.getByLabel('Your message', { exact: true })).toHaveValue('First text plus more');
  expect(events.filter(e => e.method === 'POST' && e.path.endsWith('/messages'))).toHaveLength(1);
});

test('a draft addressed to a vanished session blocks sending until a new recipient is chosen', async ({ page }) => {
  const events = await fixture(page);
  let directory = [
    { id: 'gone-1', displayName: 'Leaving Agent', deviceId: 'device-1', deviceName: 'Laptop', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' },
    { id: 'stays-1', displayName: 'Staying Agent', deviceId: 'device-2', deviceName: 'Desktop', reachable: true, deliveryMode: 'poll', updatedAt: '2026-10-24T12:00:00Z' },
  ];
  await page.route('**/api/workspaces/workspace-1/sessions*', route => route.fulfill({ json: { ok: true, result: { sessions: directory } } }));
  await page.goto('/app');
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  const sendTo = page.locator('select[name="to"]');
  await sendTo.selectOption('gone-1');
  await page.getByLabel('Your message', { exact: true }).fill('Hello there');
  directory = [directory[1]];
  await page.getByRole('button', { name: 'Refresh conversation', exact: true }).click();
  await expect(sendTo).toHaveValue('');
  await expect(sendTo.locator('option[value=""]')).toHaveText('Recipient unavailable — choose a session');
  await expect(page.getByLabel('Your message', { exact: true })).toHaveValue('Hello there');
  await page.getByRole('button', { name: 'Send request', exact: true }).click();
  expect(events.filter(e => e.method === 'POST')).toHaveLength(0);
  await expect(page.getByLabel('Your message', { exact: true })).toHaveValue('Hello there');
});

test('a retry after navigation reuses the failed send idempotency key', async ({ page }) => {
  await fixture(page);
  const postedKeys = [];
  await page.route('**/api/workspaces/workspace-1/messages*', async route => {
    if (route.request().method() === 'POST' && postedKeys.length === 0) {
      postedKeys.push(route.request().postDataJSON().idempotencyKey);
      return route.fulfill({ status: 500, json: { ok: false, error: { message: 'Delivery uncertain. Please retry.' } } });
    }
    if (route.request().method() === 'POST') postedKeys.push(route.request().postDataJSON().idempotencyKey);
    await route.fallback();
  });
  await page.goto('/app');
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  await page.getByLabel('Your message', { exact: true }).fill('Retry after navigation.');
  await page.getByRole('button', { name: 'Send request', exact: true }).click();
  await expect(page.getByText('Delivery uncertain. Please retry.', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Tasks', exact: true }).click();
  await page.getByRole('button', { name: 'Conversations', exact: true }).click();
  await expect(page.getByLabel('Your message', { exact: true })).toHaveValue('Retry after navigation.');
  await page.getByRole('button', { name: 'Send request', exact: true }).click();
  await expect(page.getByText('Retry after navigation.', { exact: true })).toBeVisible();
  expect(postedKeys).toHaveLength(2);
  expect(postedKeys[1]).toBe(postedKeys[0]);
});
