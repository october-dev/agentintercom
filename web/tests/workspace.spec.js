import { test, expect } from '@playwright/test';
import { createHash } from 'node:crypto';

async function fixture(page, signedIn = true) {
  const user = { id: 'owner-id', email: 'owner@example.test', aud: 'authenticated', role: 'authenticated', email_confirmed_at: '2026-01-01T00:00:00Z' };
  const events = [];
  let revoked = false;
  const messages = [];
  const tasks = [];
  const channels = [], posts = [], files = [];
  let answered = false;
  if (signedIn) await page.addInitScript(user => {
    const claims = btoa(JSON.stringify({ sub: user.id, aud: 'authenticated', exp: Math.floor(Date.now() / 1000) + 3600 }));
    localStorage.setItem('october-bus-auth', JSON.stringify({ access_token: 'e30.' + claims + '.fixture', refresh_token: 'fixture-refresh', expires_at: Math.floor(Date.now() / 1000) + 3600, expires_in: 3600, token_type: 'bearer', user }));
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
    else if (path.endsWith('/devices')) result = [{ id: 'device-1', name: 'My AI app', kind: 'mcp', userId: user.id, revokedAt: revoked ? '2026-10-24T12:00:00Z' : '', expiresAt: '2026-11-24T12:00:00Z' }];
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
  await page.screenshot({ path: '/tmp/october-bus-workspace-mobile.png', fullPage: true });
});

test('signed-out page presents account login without sending an email on load', async ({ page }) => {
  const events = await fixture(page, false);
  await page.goto('/app');
  await expect(page.getByRole('heading', { name: 'Sign in to October' })).toBeVisible();
  await expect(page.getByLabel('Email address', { exact: true }).first()).toBeVisible();
  expect(events).toHaveLength(0);
  await page.screenshot({ path: '/tmp/october-bus-signin.png', fullPage: true });
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
  await expect(page.getByText(/october-bus hosted login --server/)).toBeVisible();
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
