import { AuthClient } from '@supabase/auth-js';

const root = document.querySelector('#app');
const notice = document.querySelector('#notice');
const state = { auth: null, config: null, user: null, workspaces: [], workspace: null, tab: 'sessions', generation: 0, conversationAgent: '', search: '', channelId: '', threadId: '', channelSearch: '' };
let noticeTimer;
const viewCleanups = [];
function clearView() { for (const cleanup of viewCleanups.splice(0)) cleanup(); }
const params = new URLSearchParams(location.search);
if (params.has('billing')) state.tab = 'billing';
const pendingKey = 'agentintercom-pending';
let pending = {};
try { const saved = JSON.parse(sessionStorage.getItem(pendingKey) || '{}'); if (saved.until > Date.now()) pending = saved; } catch {}
if (params.get('authorization_id')) pending.authorization = params.get('authorization_id');
const fragment = new URLSearchParams(location.hash.slice(1));
const invite = fragment.get('invite');
const device = fragment.get('device') || params.get('device_code');
if (device) { pending.device = device; history.replaceState(null, '', location.pathname + location.search); }
if (invite) { pending.invite = invite; history.replaceState(null, '', location.pathname + location.search); }
if (pending.authorization || pending.invite || pending.device) sessionStorage.setItem(pendingKey, JSON.stringify({ ...pending, until: Date.now() + 20 * 60 * 1000 }));

function el(tag, attributes = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attributes)) {
    if (value == null || value === false) continue;
    if (key.startsWith('on')) node.addEventListener(key.slice(2), value);
    else if (key === 'class') node.className = value;
    else if (key === 'text') node.textContent = value;
    else node.setAttribute(key, value === true ? '' : String(value));
  }
  for (const child of children.flat(Infinity)) if (child != null) node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  return node;
}
function inform(message, error = false) {
  clearTimeout(noticeTimer); notice.textContent = message; notice.hidden = false; notice.className = error ? 'error' : '';
  notice.setAttribute('role', error ? 'alert' : 'status');
  noticeTimer = setTimeout(() => { notice.hidden = true; }, error ? 15000 : 6000);
}
async function action(button, fn) {
  if (button?.disabled) return;
  if (button) button.disabled = true;
  try { return await fn(); } catch (error) { inform(error.message || 'Something went wrong. Please try again.', true); }
  finally { if (button) button.disabled = false; }
}
function button(text, fn, className = '') { return el('button', { type: 'button', class: className, onclick: e => action(e.currentTarget, fn) }, text); }
function field(label, options = {}) { const input = el(options.multiline ? 'textarea' : 'input', { ...options, multiline: null }); return { input, node: el('label', {}, label, input) }; }
function form(children, label, submit) {
  const send = el('button', { type: 'submit', class: 'primary' }, label);
  return el('form', { onsubmit: e => { e.preventDefault(); action(send, () => submit(new FormData(e.currentTarget))); } }, children, send);
}
const brand = () => el('div', { class: 'brand' }, el('span', { class: 'mark', 'aria-hidden': true }, 'o'), 'agent intercom');
const date = value => value ? new Date(value).toLocaleString() : '—';
const path = suffix => `/api/workspaces/${encodeURIComponent(state.workspace.id)}${suffix}`;
async function api(url, options = {}) {
  const headers = { 'Content-Type': 'application/json' };
  if (state.auth && url !== '/api/config') {
    const { data, error } = await state.auth.getSession();
    if (error) throw error;
    if (data.session) headers.Authorization = `Bearer ${data.session.access_token}`;
  }
  const response = await fetch(url, { method: options.method || 'GET', credentials: 'omit', headers, body: options.body == null ? undefined : JSON.stringify(options.body), signal: AbortSignal.timeout(options.timeout || 15000) });
  const data = await response.json();
  if (!response.ok || data.ok === false || data.error) throw new Error(data.error?.message || 'The request could not be completed. Please try again.');
  return data.result;
}
async function copy(value) { await navigator.clipboard.writeText(value); inform('Copied to clipboard.'); }

const uploadKeys = new WeakMap();
async function fileRequest(url, options = {}) {
  const { data, error } = await state.auth.getSession();
  if (error) throw error;
  if (!data.session) throw new Error('Sign in again to access workspace files.');
  const response = await fetch(url, { ...options, credentials: 'omit', redirect: 'error', headers: { Authorization: `Bearer ${data.session.access_token}`, 'Content-Type': 'application/octet-stream' }, signal: AbortSignal.timeout(30000) });
  if (!response.ok) { const failure = await response.json().catch(() => ({})); throw new Error(failure.error?.message || 'File transfer failed. Please try again.'); }
  return response;
}
async function uploadFile(scopePath, file) {
  if (file.size > 16 * 1024 * 1024) throw new Error('Files can be up to 16 MiB.');
  if (!uploadKeys.has(file)) uploadKeys.set(file, crypto.randomUUID());
  const query = new URLSearchParams({ name: file.name, mediaType: file.type.split(';')[0] || 'application/octet-stream', idempotencyKey: uploadKeys.get(file) });
  const response = await fileRequest(scopePath + '/attachments?' + query, { method: 'POST', body: file });
  const data = await response.json(); if (!data.ok) throw new Error(data.error?.message || 'Upload failed.'); return data.result;
}
async function downloadFile(scopePath, id, name) {
  const response = await fileRequest(scopePath + '/attachments/' + encodeURIComponent(id) + '/content');
  const data = await response.arrayBuffer();
  const digest = [...new Uint8Array(await crypto.subtle.digest('SHA-256', data))].map(byte => byte.toString(16).padStart(2, '0')).join('');
  if (digest !== response.headers.get('X-Content-SHA256')) throw new Error('File checksum did not match. The download was stopped.');
  const disposition = response.headers.get('Content-Disposition') || '';
  if (!name) { try { name = decodeURIComponent(disposition.match(/filename\*=utf-8''([^;]+)/i)?.[1] || '') || disposition.match(/filename="([^"]+)"/)?.[1] || disposition.match(/filename=([^;]+)/)?.[1]; } catch {} }
  const url = URL.createObjectURL(new Blob([data], { type: 'application/octet-stream' }));
  const link = el('a', { href: url, download: (name || id).replace(/[\\/\x00-\x1f]/g, '_') }); document.body.append(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(url), 30000);
}
function attachmentPicker(scopePath, textInput) {
  const file = field('Attach a file', { type: 'file' });
  const attach = button('Upload and attach', async () => {
    const selected = file.input.files[0]; if (!selected) throw new Error('Choose a file first.');
    const submit = file.input.closest('form')?.querySelector('[type="submit"]'); if (submit) submit.disabled = true;
    try { const uploaded = await uploadFile(scopePath, selected); textInput.value += `${textInput.value ? '\n\n' : ''}${uploaded.name}: ${uploaded.uri}`; file.input.value = ''; inform('File attached. Send your message to share the reference.'); }
    finally { if (submit) submit.disabled = false; }
  });
  return el('details', {}, el('summary', {}, 'Attach a file'), file.node, attach, el('p', { class: 'help' }, 'Up to 16 MiB. Uploaded files are visible to everyone in this workspace.'));
}
function attachmentLinks(scopePath, text, context = []) {
  const ids = new Map();
  for (const match of String(text).matchAll(/october-bus:\/\/attachments\/(att_[0-9a-f]{32})(?![0-9a-f])/g)) ids.set(match[1], '');
  for (const item of context || []) { const id = item.uri?.match(/^october-bus:\/\/attachments\/(att_[0-9a-f]{32})$/)?.[1]; if (id) ids.set(id, item.title); }
  return el('div', { class: 'actions' }, [...ids].map(([id, name]) => button(name ? `Download ${name}` : 'Download shared file', () => downloadFile(scopePath, id, name))));
}
async function filesView() {
  const scopePath = path('');
  const container = el('div'), list = el('div', { class: 'grid' });
  const append = page => { for (const file of page.attachments) {
    const own = file.authorKind === 'human' && file.authorId === state.user.id;
    const remove = own || ['owner', 'admin'].includes(state.workspace.role) ? button('Delete file', () => modal('Delete this file?', close => el('div', {}, el('p', {}, `Delete ${file.name}? Existing message references will stop working.`), button('Delete permanently', async () => { await api(scopePath + '/attachments/' + file.id, { method: 'DELETE' }); close(); await render(); })))) : null;
    list.append(el('article', { class: 'card' }, el('h2', {}, file.name), el('p', { class: 'help' }, `${(file.size / 1024).toFixed(1)} KiB · ${file.mediaType} · ${date(file.createdAt)}`), el('div', { class: 'actions' }, button('Download', () => downloadFile(scopePath, file.id, file.name)), button('Copy reference', () => copy(file.uri)), remove)));
  } };
  const page = await api(scopePath + '/attachments'); append(page);
  if (!page.attachments.length) list.append(el('section', { class: 'card empty' }, el('h2', {}, 'Share the files your work needs.'), el('p', {}, 'Upload a file here or attach one to a conversation. Files stay private to this workspace.')));
  const file = field('File to upload', { type: 'file', required: true });
  container.append(el('section', { class: 'card' }, form([file.node, el('p', { class: 'help' }, 'Up to 16 MiB per file. Everyone in the workspace can download it.')], 'Upload file', async () => { const selected = file.input.files[0]; if (!selected) throw new Error('Choose a file first.'); await uploadFile(scopePath, selected); await render(); })), list);
  if (page.nextCursor) { let cursor = page.nextCursor; const more = button('More files', async () => { const next = await api(scopePath + '/attachments?after=' + encodeURIComponent(cursor)); append(next); cursor = next.nextCursor; if (!cursor) more.remove(); }); container.append(more); }
  return container;
}
function closePending(key) { delete pending[key]; sessionStorage.setItem(pendingKey, JSON.stringify({ ...pending, until: Date.now() + 20 * 60 * 1000 })); history.replaceState(null, '', '/app'); }
function modal(title, build) {
  const dialog = el('dialog', {}, el('h2', {}, title));
  const close = () => { dialog.close(); dialog.remove(); };
  dialog.append(build(close), el('div', { class: 'actions end' }, button('Close', close)));
  dialog.addEventListener('close', () => dialog.remove()); document.body.append(dialog); dialog.showModal(); return dialog;
}
function secretDialog(title, value, description) {
  modal(title, () => el('div', {}, el('p', {}, description), el('pre', { class: 'secret' }, value), button('Copy', () => copy(value))));
}

function signIn() {
  clearView();
  state.user = null;
  const email = field('Email address', { type: 'email', name: 'email', autocomplete: 'email', required: true });
  const emailForm = form([email.node], 'Continue with email', async data => {
    const { error } = await state.auth.signInWithOtp({ email: data.get('email'), options: { emailRedirectTo: location.origin + '/app' + (pending.authorization ? '?authorization_id=' + encodeURIComponent(pending.authorization) : pending.device ? '?device_code=' + encodeURIComponent(pending.device) : ''), shouldCreateUser: true } });
    if (error) throw error;
    inform('Check your email for an October sign-in link. Open it in this browser.');
    emailForm.append(el('p', { class: 'notice-inline' }, 'Check your email for a sign-in link. Keep this tab open so you can return to your connection request.'));
  });
  const passwordEmail = field('Email address', { name: 'email', type: 'email', autocomplete: 'username', required: true });
  const password = field('Password', { name: 'password', type: 'password', autocomplete: 'current-password', required: true });
  const passwordForm = form([passwordEmail.node, password.node], 'Sign in', async data => {
    const { error } = await state.auth.signInWithPassword({ email: data.get('email'), password: data.get('password') });
    if (error) throw error;
    await signedIn();
  });
  root.replaceChildren(el('main', { id: 'main', class: 'auth-wrap' }, brand(), el('div', { class: 'auth-layout' },
    el('div', { class: 'auth-copy' }, el('div', { class: 'eyebrow' }, 'One place to work together'), el('h1', {}, 'Your agents.\nA shared conversation.'), el('p', {}, 'Connect your AI apps, bring in another machine, and keep the work moving with your team.')),
    el('section', { class: 'card auth-card', 'aria-labelledby': 'signin-title' }, el('h2', { id: 'signin-title' }, 'Sign in to October'), el('p', { class: 'help' }, 'Use your existing October account, or create one with your email.'), emailForm, el('details', {}, el('summary', {}, 'Use an existing password'), passwordForm), el('p', { class: 'help' }, 'Your AI subscriptions stay with your apps. Agent Intercom connects the work.')))));
}

async function signedIn() {
  state.user = await api('/api/me');
  state.workspaces = await api('/api/workspaces');
  const wanted = (params.has('billing') && params.get('workspace')) || localStorage.getItem('agentintercom-workspace');
  state.workspace = state.workspaces.find(w => w.id === wanted) || state.workspaces[0] || null;
  if (pending.invite) return invitation();
  if (pending.authorization) return consent();
  if (pending.device) return deviceConsent();
  if (!state.workspace) return firstWorkspace();
  await render();
}
function createWorkspace(onCreated) {
  const name = field('Workspace name', { name: 'name', placeholder: 'My team', maxlength: 100, required: true });
  return form([name.node, el('p', { class: 'help' }, 'Your first workspace starts a 14-day trial. Additional workspaces share its end date. Up to 5 people; no payment details needed.')], 'Create workspace', async data => {
    const workspace = await api('/api/workspaces', { method: 'POST', body: { name: data.get('name') } });
    state.workspaces.push(workspace); state.workspace = workspace; localStorage.setItem('agentintercom-workspace', workspace.id); await onCreated();
  });
}
function firstWorkspace() {
  root.replaceChildren(el('main', { id: 'main', class: 'auth-wrap' }, brand(), el('section', { class: 'card consent' }, el('h1', {}, 'Make room for your team.'), el('p', {}, 'A workspace holds your connected sessions, messages and shared work.'), createWorkspace(render), button('Sign out', signOut))));
}
async function signOut() { const { error } = await state.auth.signOut({ scope: 'local' }); if (error) throw error; state.generation++; signIn(); }
function workspaceSelect(onChange) {
  const select = el('select', { 'aria-label': 'Workspace', onchange: e => { state.workspace = state.workspaces.find(w => w.id === e.target.value); localStorage.setItem('agentintercom-workspace', state.workspace.id); action(null, onChange); } }, state.workspaces.map(w => el('option', { value: w.id, selected: w.id === state.workspace?.id }, w.name)));
  return select;
}
async function consent() {
  const request = await api(`/api/oauth/requests/${encodeURIComponent(pending.authorization)}`);
  const workspaceId = state.workspace?.id;
  const devices = workspaceId ? await api(path('/devices')) : [];
  const ownApps = devices.filter(device => device.userId === state.user.id && device.kind === 'mcp');
  const replacement = el('select', { 'aria-label': 'App connection' }, el('option', { value: '' }, 'Create a new app connection'), ownApps.map(device => el('option', { value: device.id }, `Reconnect ${device.name}${device.revokedAt ? ' · revoked' : ''}`)));
  const warning = el('p', { class: 'notice-inline', hidden: true }, 'The old app credentials will stop working. This app will receive the same session inbox, including its pending messages.');
  replacement.addEventListener('change', () => { warning.hidden = !replacement.value; });
  const approve = async allowed => {
    if (allowed && workspaceId !== state.workspace?.id) throw new Error('The selected workspace changed. Wait for its connections to load, then try again.');
    const result = await api(`/api/oauth/requests/${encodeURIComponent(pending.authorization)}`, { method: 'POST', body: { approve: allowed, workspaceId: allowed ? workspaceId : '', ...(allowed && replacement.value ? { replaceDeviceId: replacement.value } : {}) } });
    closePending('authorization'); location.assign(result.redirectUrl);
  };
  const choice = state.workspaces.length ? el('div', {}, el('label', {}, 'Connect to workspace', workspaceSelect(consent)), ownApps.length ? el('label', {}, 'App connection', replacement) : null, warning, el('div', { class: 'actions' }, button('Allow connection', () => approve(true), 'primary'), button('Cancel', () => approve(false)))) : createWorkspace(consent);
  root.replaceChildren(el('main', { id: 'main', class: 'consent' }, brand(), el('section', { class: 'card' }, el('div', { class: 'eyebrow' }, 'Connect an app'), el('h1', {}, request.clientName), el('p', {}, 'wants to connect to your Agent Intercom workspace.'), el('p', { class: 'help' }, 'This app registered itself. October has not verified its publisher. Only continue if you started this connection in an app you trust.'), el('ul', { class: 'permission-list' }, el('li', {}, 'Read and share messages, files and channels in the selected workspace.'), el('li', {}, 'Discover connected sessions and coordinate shared tasks.'), el('li', {}, 'Remain connected until you revoke access or the connection expires.')), el('p', { class: 'help' }, 'You can revoke access at any time in Members & devices.'), el('p', { class: 'help' }, 'Returns to: ', el('code', {}, new URL(request.redirectUri).origin)), choice)));
}
async function deviceConsent() {
  const request = await api(`/api/device/authorizations/${encodeURIComponent(pending.device)}`);
  const workspaceId = state.workspace?.id;
  const devices = workspaceId ? await api(path('/devices')) : [];
  const ownMachines = devices.filter(device => device.userId === state.user.id && device.kind === 'device');
  const replacement = el('select', { 'aria-label': 'Device connection' }, el('option', { value: '' }, 'Connect a new machine'), ownMachines.map(device => el('option', { value: device.id }, `Replace ${device.name}${device.revokedAt ? ' · revoked' : ''}`)));
  const warning = el('p', { class: 'notice-inline', hidden: true }, 'Replacing a connection signs out its existing sessions. Their inboxes stay available. Resume each session using its existing session ID after this login completes.');
  replacement.addEventListener('change', () => { warning.hidden = !replacement.value; });
  const approve = async allowed => {
    if (allowed && workspaceId !== state.workspace?.id) throw new Error('The selected workspace changed. Wait for its connections to load, then try again.');
    await api(`/api/device/authorizations/${encodeURIComponent(pending.device)}`, { method: 'POST', body: { approve: allowed, workspaceId: allowed ? workspaceId : '', ...(allowed && replacement.value ? { replaceDeviceId: replacement.value } : {}) } });
    closePending('device');
    root.replaceChildren(el('main', { id: 'main', class: 'consent' }, brand(), el('section', { class: 'card' }, el('h1', {}, allowed ? 'Your device can connect.' : 'Connection declined.'), el('p', {}, allowed ? 'Return to your terminal to finish. Each session on this device will appear in your workspace.' : 'No device credential was issued.'), button('Open workspace', signedIn, 'primary'))));
  };
  const choice = state.workspaces.length ? el('div', {}, el('label', {}, 'Connect to workspace', workspaceSelect(deviceConsent)), ownMachines.length ? el('label', {}, 'Device connection', replacement) : null, warning, el('div', { class: 'actions' }, button('Connect this device', () => approve(true), 'primary'), button('Decline', () => approve(false)))) : createWorkspace(deviceConsent);
  root.replaceChildren(el('main', { id: 'main', class: 'consent' }, brand(), el('section', { class: 'card' }, el('div', { class: 'eyebrow' }, 'Connect a machine'), el('h1', {}, request.name), el('p', {}, 'Check that this code matches the one in your terminal:'), el('pre', {}, request.userCode), el('p', { class: 'help' }, 'Only approve a request you started. This device can read and send workspace messages and start its own agent sessions. You can revoke it in Members & devices.'), choice)));
}
function invitation() {
  root.replaceChildren(el('main', { id: 'main', class: 'consent' }, brand(), el('section', { class: 'card' }, el('h1', {}, 'Join a workspace'), el('p', {}, `You are signed in as ${state.user.email}. The invitation must match this address.`), el('div', { class: 'actions' }, button('Accept invitation', async () => {
    const result = await api('/api/invites/accept', { method: 'POST', body: { token: pending.invite } });
    localStorage.setItem('agentintercom-workspace', result.workspaceId); closePending('invite'); await signedIn();
  }, 'primary'), button('Use another account', signOut), button('Cancel', async () => { closePending('invite'); await signedIn(); })))));
}

const titles = { sessions: ['Sessions', 'Choose the connected session you want to work with.'], messages: ['Conversations', 'Messages and receipts shared with this workspace.'], channels: ['Channels', 'Shared discussions and threads for this workspace.'], tasks: ['Tasks', 'Shared work, claimed by the session doing it.'], questions: ['Needs you', 'Help your agents continue when they need a decision.'], files: ['Files', 'Private files shared with your workspace.'], connect: ['Connect an app', 'Use your favorite AI app, on this machine or another one.'], members: ['Members & devices', 'Give each person and machine their own access.'], data: ['Data & recovery', 'Export your workspace and manage retained history.'], billing: ['Billing & usage', 'Your workspace plan, payment details and included usage.'] };
async function render() {
  clearView();
  if (!state.workspace) return firstWorkspace();
  const generation = ++state.generation;
  const main = el('main', { id: 'main', class: 'content' });
  const tabs = el('nav', { 'aria-label': 'Workspace' }, Object.entries(titles).map(([key, [title]]) => button(title, async () => { state.tab = key; await render(); }, key === state.tab ? 'selected' : '')));
  [...tabs.children].forEach((node, i) => { if (Object.keys(titles)[i] === state.tab) node.setAttribute('aria-current', 'page'); });
  root.replaceChildren(el('div', { class: 'shell' }, el('aside', { class: 'sidebar' }, brand(), el('div', { class: 'workspace-switch' }, workspaceSelect(render), button('+ New workspace', () => modal('New workspace', close => createWorkspace(async () => { close(); await render(); })), 'subtle-button')), tabs, el('div', { class: 'account' }, el('p', {}, state.user.email), button('Sign out', signOut, 'subtle-button'))), main));
  main.append(el('header', { class: 'topline' }, el('div', {}, el('div', { class: 'eyebrow' }, state.workspace.name), el('h1', {}, titles[state.tab][0]), el('p', { class: 'muted' }, titles[state.tab][1])), state.tab === 'sessions' ? button('Connect an app', async () => { state.tab = 'connect'; await render(); }, 'primary') : null));
  const loading = el('p', { class: 'progress' }, 'Loading…'); main.append(loading);
  try {
    const view = await ({ data: dataView, billing: billingView, files: filesView, channels: channelsView, messages: messagesView, tasks: tasksView, questions: questionsView, sessions: sessionsView, connect: connectView, members: membersView })[state.tab]();
    if (generation !== state.generation) return;
    loading.replaceWith(view);
  } catch (error) { if (generation === state.generation) loading.replaceWith(el('div', { class: 'card' }, el('p', {}, error.message), button('Try again', render))); }
}
async function sessionsView() {
  const container = el('div');
  const list = el('div', { class: 'grid' });
  const page = await api(path('/sessions'));
  const append = data => {
    for (const session of data.sessions) {
      const online = session.reachable === true;
      list.append(el('article', { class: 'card' }, el('div', { class: 'actions' }, el('h2', {}, session.displayName || session.agentId || session.id), el('span', { class: `badge ${online ? 'live' : ''}` }, online ? 'Connected' : 'Offline')), el('p', { class: 'help' }, session.deviceName), el('p', { class: 'help' }, session.deliveryMode === 'poll' ? 'Inbox available when this app checks for messages. It may need you to resume the conversation.' : 'Native listener connected. Your app’s permissions still apply.'), el('small', {}, `Last seen ${date(session.updatedAt)}`), session.wake?.uncertain ? el('p', { class: 'notice-inline' }, 'A wake submission is uncertain. Inspect the app before retrying; the message is still in its inbox.') : null, el('div', { class: 'actions' }, button('Open conversation', async () => { state.conversationAgent = session.id; state.tab = 'messages'; await render(); }))));
      if (session.userId === state.user.id) list.lastChild.append(el('details', {}, el('summary', {}, 'Session details'), el('p', { class: 'help' }, 'This ID identifies the inbox. Terminal sessions can resume it with --id after reconnecting their owning machine.'), el('code', {}, session.id), button('Copy session ID', () => copy(session.id))));
    }
  };
  if (!page.sessions.length) return el('section', { class: 'card empty' }, el('h2', {}, 'Your first connection starts here.'), el('p', { class: 'muted' }, 'Connect an AI app or terminal session. It will appear here under its own name, ready for shared work.'), button('Connect an app', async () => { state.tab = 'connect'; await render(); }, 'primary'));
  append(page); container.append(list);
  if (page.nextCursor) { let cursor = page.nextCursor; const more = button('Show more sessions', async () => { const next = await api(path('/sessions') + '?after=' + encodeURIComponent(cursor)); append(next); cursor = next.nextCursor; if (!cursor) more.remove(); }); container.append(more); }
  container.append(el('p', { class: 'help' }, 'Connected means the app contacted Agent Intercom recently. It does not mean the model is currently working.'), button('Refresh sessions', render)); return container;
}
function connectView() {
  const url = state.config.mcpUrl;
  const scopePath = path('');
  return el('div', { class: 'grid' }, el('section', { class: 'card' }, el('h2', {}, 'An app with remote MCP'), el('p', {}, 'In your app’s connectors or MCP settings, add Agent Intercom using this address.'), el('pre', {}, url), button('Copy connector URL', () => copy(url)), el('ol', {}, el('li', {}, 'Add the connector in your app.'), el('li', {}, 'Sign in to October and choose this workspace.'), el('li', {}, 'Ask your app to check its Bus inbox or find connected sessions.')), el('p', { class: 'help' }, 'Works with compatible Streamable HTTP MCP clients. Your app controls which conversations can use tools and whether an idle conversation can resume.')),
    el('section', { class: 'card' }, el('h2', {}, 'An app that asks for an API key'), el('p', {}, 'Use the same remote MCP URL. Create a separate key for each app so you can revoke it independently.'), form([field('App connection name', { name: 'name', required: true, maxlength: 100, placeholder: 'My AI app' }).node, el('p', { class: 'help' }, 'The app can read and share workspace messages, files, channels and tasks. A key expires after 90 days and cannot manage your account or members.')], 'Create MCP key', async data => {
      const connection = await api(scopePath + '/mcp-keys', { method: 'POST', body: { name: data.get('name') } });
      secretDialog('MCP connection key', connection.token, `Paste this into your app’s secret/API key field, or use Authorization: Bearer <key>. It is shown once and expires ${date(connection.expiresAt)}. Keep it out of chat, URLs and source control.`);
    }), el('p', { class: 'help' }, 'Choose OAuth instead if your app offers it. Neither connection method can make an unsupported app resume an idle conversation.')),
    el('section', { class: 'card' }, el('h2', {}, 'Another machine or automation'), el('p', {}, 'Install the Agent Intercom CLI on that machine, then run:'), el('pre', {}, 'agentintercom hosted login --server ' + location.origin), el('p', {}, 'Open the link it prints, confirm the code and choose this workspace. For any app with a local MCP connection, run:'), el('pre', {}, 'agentintercom connect --hosted default'), el('p', {}, 'Or start a terminal session with native message wake:'), el('pre', {}, 'agentintercom session start claude --hosted default\nagentintercom session start codex --hosted default'), el('p', { class: 'help' }, 'Choose the app you use. Keep its terminal open; normal app permissions apply. Codex native wake is experimental.'), el('details', {}, el('summary', {}, 'Create a credential for custom automation'), form([field('Device name', { name: 'name', required: true, maxlength: 100, placeholder: 'My laptop' }).node], 'Create device credential', async data => {
      const device = await api(path('/devices'), { method: 'POST', body: { name: data.get('name') } });
      secretDialog('Device credential', device.token, 'Save this in your device’s secret store. It is shown only once. Anyone with it can join sessions to this workspace as you.');
    }), el('p', { class: 'help' }, 'Keep credentials out of chat messages, shell history and source control.'))));
}
async function membersView() {
  const scopePath = path('');
  const [members, devices] = await Promise.all([api(path('/members')), api(path('/devices'))]);
  const canManage = state.workspace.role !== 'member';
  const memberList = el('section', { class: 'card' }, el('h2', {}, 'People'));
  for (const member of members) {
    const controls = [];
    if (state.workspace.role === 'owner') {
      const select = el('select', { 'aria-label': `Role for ${member.email}` }, ['member', 'admin', 'owner'].map(role => el('option', { value: role, selected: role === member.role }, role)));
      controls.push(select, button('Save role', async () => { await api(path(`/members/${encodeURIComponent(member.userId)}`), { method: 'PATCH', body: { role: select.value } }); await signedIn(); }));
    } else controls.push(el('span', { class: 'badge' }, member.role));
    if (canManage || member.userId === state.user.id) controls.push(button(member.userId === state.user.id ? 'Leave' : 'Remove', () => modal('Remove workspace access?', close => el('div', {}, el('p', {}, `${member.email} will lose access and their connected sessions will be revoked.`), button('Remove access', async () => { await api(path(`/members/${encodeURIComponent(member.userId)}`), { method: 'DELETE' }); close(); await signedIn(); }, 'danger'))), 'danger'));
    memberList.append(el('div', { class: 'row' }, el('div', { class: 'details' }, el('strong', {}, member.email), el('p', { class: 'help' }, `Joined ${date(member.joinedAt)}`)), el('div', { class: 'actions' }, controls)));
  }
  const deviceList = el('section', { class: 'card' }, el('h2', {}, 'Apps & devices'));
  if (!devices.length) deviceList.append(el('p', { class: 'muted' }, 'No devices connected yet.'));
  for (const device of devices) {
    const revoked = !!device.revokedAt;
    const controls = [];
    if (device.kind === 'mcp_key' && device.userId === state.user.id) controls.push(button('Replace key', () => modal('Replace this MCP key?', close => el('div', {}, el('p', {}, `The old key for ${device.name} will stop working immediately. The replacement keeps its session inbox and pending messages. Update the key in your app after replacing it.`), button('Create replacement key', async () => {
      const connection = await api(scopePath + '/mcp-keys', { method: 'POST', body: { name: device.name, replaceDeviceId: device.id } }); close();
      secretDialog('Replacement MCP key', connection.token, `Update your app’s secret/API key field. Shown once; expires ${date(connection.expiresAt)}. If this value is lost, replace it again from Members & devices.`);
    }, 'primary')))));
    if (!revoked && (canManage || device.userId === state.user.id)) controls.push(button('Revoke', () => modal('Revoke connection?', close => el('div', {}, el('p', {}, `${device.name} will stop receiving access to this workspace.`), button('Revoke connection', async () => { await api(scopePath + `/devices/${encodeURIComponent(device.id)}`, { method: 'DELETE' }); close(); await render(); }, 'danger'))), 'danger'));
    deviceList.append(el('div', { class: 'row' }, el('div', { class: 'details' }, el('strong', {}, device.name), el('p', { class: 'help' }, `${device.kind === 'mcp' ? 'MCP app' : device.kind === 'mcp_key' ? 'MCP key' : 'Device'} · ${revoked ? 'Revoked' : `Expires ${date(device.expiresAt)}`}`)), el('div', { class: 'actions' }, controls)));
  }
  const container = el('div', {}, memberList, deviceList);
  if (canManage) {
    const email = field('Teammate’s email', { name: 'email', type: 'email', required: true });
    const invitationForm = form([email.node], 'Create invitation', async data => {
      const result = await api(path('/invites'), { method: 'POST', body: { email: data.get('email'), role: 'member' } });
      secretDialog('Share this invitation', `${location.origin}/app#invite=${encodeURIComponent(result.token)}`, `Share this link with ${result.email}. It expires ${date(result.expiresAt)} and can be accepted once by that address. October has not sent an email.`);
    });
    const invites = await api(path('/invites'));
    const section = el('section', { class: 'card' }, el('h2', {}, 'Invite a teammate'), invitationForm);
    for (const item of invites) section.append(el('div', { class: 'row' }, el('div', {}, el('strong', {}, item.email), el('p', { class: 'help' }, `Expires ${date(item.expiresAt)}`)), button('Revoke invitation', async () => { await api(path(`/invites/${encodeURIComponent(item.id)}`), { method: 'DELETE' }); await render(); }, 'danger')));
    container.append(section);
  }
  return container;
}

async function start() {
  state.config = await api('/api/config');
  if (!state.config.authUrl || !state.config.authPublicKey) throw new Error('Sign-in is not configured on this server. Contact its operator.');
  state.auth = new AuthClient({ url: state.config.authUrl + '/auth/v1', headers: { apikey: state.config.authPublicKey, Authorization: `Bearer ${state.config.authPublicKey}` }, storageKey: 'agentintercom-auth', flowType: 'pkce', autoRefreshToken: true, persistSession: true, detectSessionInUrl: true });
  const { data, error } = await state.auth.getSession();
  if (error) throw error;
  if (data.session) await signedIn(); else signIn();
  state.auth.onAuthStateChange((event, session) => {
    // Never call the Auth client while its callback holds the SDK's lock.
    if (event === 'SIGNED_OUT') setTimeout(() => { state.generation++; signIn(); }, 0);
    if (event === 'SIGNED_IN' && session && !state.user) setTimeout(() => action(null, signedIn), 0);
  });
}
start().catch(error => { root.replaceChildren(el('main', { id: 'main', class: 'auth-wrap' }, brand(), el('section', { class: 'card consent' }, el('h1', {}, 'Couldn’t open your workspace'), el('p', {}, error.message), button('Try again', () => location.reload())))); });

async function messagesView() {
  const generation = state.generation;
  const scopePath = path('');
  const [sessionsPage, members] = await Promise.all([api(scopePath + '/sessions'), api(scopePath + '/members')]);
  const sessions = sessionsPage.sessions;
  const names = new Map(sessions.map(s => ['agent:' + s.id, s.displayName]));
  for (const member of members) names.set('human:' + member.userId, member.email);
  const name = (kind, id) => names.get(kind + ':' + id) || id;
  const query = new URLSearchParams();
  if (state.conversationAgent) query.set('agentId', state.conversationAgent);
  if (state.search) query.set('query', state.search);
  const filter = el('select', { 'aria-label': 'Filter by session', onchange: e => { state.conversationAgent = e.target.value; action(null, render); } }, el('option', { value: '' }, 'All sessions'), sessions.map(s => el('option', { value: s.id, selected: s.id === state.conversationAgent }, s.displayName)));
  const search = field('Search messages', { name: 'query', value: state.search, type: 'search', maxlength: 256 });
  const searchForm = form([search.node], 'Search', async data => { state.search = data.get('query').trim(); await render(); });
  const container = el('div', {}, el('div', { class: 'card grid' }, el('label', {}, 'Conversation', filter), searchForm));
  const list = el('div', { class: 'conversation-list', 'aria-label': 'Workspace messages' });
  const rendered = new Map();
  let live = true, polling = false;
  viewCleanups.push(() => { live = false; });
  const receipt = message => message.responseMessageId ? 'Replied' : message.state === 'acknowledged' ? (message.toKind === 'human' ? 'Read' : 'Acknowledged') : message.state === 'delivered' ? 'Delivered to app' : message.state === 'expired' ? 'Expired' : 'Waiting for delivery';
  const append = (page, fresh = false) => {
    const rows = fresh ? [...page.messages].reverse() : page.messages;
    for (const message of rows) {
      const fingerprint = JSON.stringify(message);
      const previous = rendered.get(message.id);
      if (previous?.fingerprint === fingerprint) continue;
      const item = el('article', { class: 'card', id: message.id }, el('div', { class: 'row' }, el('div', { class: 'details' }, el('strong', {}, name(message.fromKind, message.from)), ' → ', name(message.toKind, message.to), el('p', { class: 'help' }, date(message.createdAt))), el('span', { class: 'badge' }, receipt(message))), el('p', { class: 'message' }, message.body));
      item.append(attachmentLinks(scopePath, message.body, message.context));
      if (message.responseTo) item.append(el('p', { class: 'help' }, 'Reply to ', el('code', {}, message.responseTo)));
      if (message.toKind === 'human' && message.to === state.user.id && message.state !== 'acknowledged' && message.state !== 'expired') item.append(button('Mark read', async () => { await api(scopePath + '/messages/read', { method: 'POST', body: { messageIds: [message.id] } }); await render(); }, 'subtle-button'));
      if (previous) previous.node.replaceWith(item);
      else if (fresh) list.prepend(item);
      else list.append(item);
      rendered.set(message.id, { fingerprint, node: item });
    }
  };
  const page = await api(scopePath + '/messages?' + query);
  append(page);
  const refreshQuery = query.toString();
  const timer = setInterval(async () => {
    if (state.generation !== generation) { clearInterval(timer); return; }
    if (!live || polling || document.hidden) return;
    polling = true;
    try { const next = await api(scopePath + '/messages?' + refreshQuery); if (live) { if (next.messages.length) list.querySelector('.empty')?.remove(); append(next, true); } }
    catch (error) { if (live) inform('Conversation refresh failed. ' + error.message, true); }
    finally { polling = false; }
  }, 5000);
  viewCleanups.push(() => clearInterval(timer));
  if (!page.messages.length) list.append(el('section', { class: 'card empty' }, el('h2', {}, state.search ? 'No matching messages' : 'Start a conversation'), el('p', { class: 'muted' }, state.search ? 'Try a different phrase or session.' : 'Choose a connected session below and send it a request.')));
  container.append(list);
  if (page.nextCursor) {
    let cursor = page.nextCursor;
    const more = button('Earlier messages', async () => { query.set('before', cursor); const older = await api(scopePath + '/messages?' + query); append(older); cursor = older.nextCursor; if (!cursor) more.remove(); });
    container.append(more);
  }
  if (sessions.length) {
    const target = el('select', { name: 'to', required: true }, sessions.map(s => el('option', { value: s.id, selected: s.id === state.conversationAgent }, s.displayName + (s.reachable ? '' : ' · offline'))));
    const body = field('Your message', { name: 'body', multiline: true, required: true, maxlength: 65536, placeholder: 'What would you like this session to help with?' });
    let idempotencyKey = crypto.randomUUID(), draft = '';
    const compose = form([el('label', {}, 'Send to', target), body.node, attachmentPicker(scopePath, body.input), el('p', { class: 'help' }, 'Visible to workspace members. An MCP app may need you to resume it or ask it to check its inbox.')], 'Send request', async data => {
      const current = JSON.stringify([data.get('to'), data.get('body')]);
      if (draft && draft !== current) idempotencyKey = crypto.randomUUID();
      draft = current;
      await api(scopePath + '/messages', { method: 'POST', body: { to: data.get('to'), body: data.get('body'), mode: 'request', idempotencyKey } });
      state.conversationAgent = data.get('to'); state.search = ''; inform('Request saved. Delivery status will update when the app checks its inbox.'); await render();
    });
    container.append(el('section', { class: 'card' }, el('h2', {}, 'Send a message'), compose));
  } else container.append(button('Connect an app', async () => { state.tab = 'connect'; await render(); }, 'primary'));
  container.append(button('Refresh conversation', render));
  return container;
}

async function tasksView() {
  const scopePath = path('');
  const page = await api(scopePath + '/tasks');
  const container = el('div'); const list = el('div', { class: 'grid' });
  const append = data => { for (const task of data.tasks) list.append(el('article', { class: 'card' }, el('div', { class: 'actions' }, el('h2', {}, task.title), el('span', { class: 'badge' }, task.status)), el('p', { class: 'message' }, task.description), task.claimedBy ? el('p', { class: 'help' }, 'Claimed by ', el('code', {}, task.claimedBy)) : el('p', { class: 'help' }, task.ready ? 'Ready for an agent to claim.' : 'Waiting for dependencies.'), task.note ? el('p', {}, task.note) : null, (task.recentProgress || []).map(progress => el('p', { class: 'help message' }, `${progress.kind}: ${progress.text}`)), el('small', {}, date(task.updatedAt)))); };
  append(page); if (!page.tasks.length) list.append(el('section', { class: 'card empty' }, el('h2', {}, 'Give the work a place to land.'), el('p', {}, 'Create a task, then ask a connected session to claim it. Claims prevent two agents from taking the same work.')));
  container.append(list);
  if (page.nextCursor) { let cursor = page.nextCursor; const more = button('More tasks', async () => { const next = await api(scopePath + '/tasks?after=' + encodeURIComponent(cursor)); append(next); cursor = next.nextCursor; if (!cursor) more.remove(); }); container.append(more); }
  const title = field('Task title', { name: 'title', maxlength: 256, required: true });
  const description = field('Description', { name: 'description', multiline: true, maxlength: 16384 });
  container.append(el('section', { class: 'card' }, el('h2', {}, 'New task'), form([title.node, description.node], 'Create task', async data => { await api(scopePath + '/tasks', { method: 'POST', body: { title: data.get('title'), description: data.get('description') } }); inform('Task created. An agent can now claim it.'); await render(); })));
  return container;
}

async function questionsView() {
  const scopePath = path('');
  const page = await api(scopePath + '/questions');
  const container = el('div');
  const append = data => {
    for (const question of data.escalations) {
      const answer = field('Your answer', { name: 'answer', multiline: true, required: true, maxlength: 16384 });
      const choices = (question.options || []).map(option => button(option, () => { answer.input.value = option; answer.input.focus(); }));
      container.append(el('section', { class: 'card' }, el('h2', { class: 'message' }, question.question), el('p', { class: 'help' }, 'From ', el('code', {}, question.agentId), ' · ', date(question.createdAt)), el('div', { class: 'actions' }, choices), form([answer.node], 'Send answer', async data => { await api(scopePath + `/questions/${encodeURIComponent(question.id)}/answer`, { method: 'POST', body: { answer: data.get('answer') } }); inform('Answer saved for the requesting session.'); await render(); })));
    }
  };
  append(page);
  if (!page.escalations.length) container.append(el('section', { class: 'card empty' }, el('h2', {}, 'Nothing waiting on you.'), el('p', { class: 'muted' }, 'When an agent asks for a decision, you can answer it here.')));
  if (page.nextCursor) { let cursor = page.nextCursor; const more = button('More questions', async () => { const next = await api(scopePath + '/questions?after=' + encodeURIComponent(cursor)); append(next); cursor = next.nextCursor; if (!cursor) more.remove(); }); container.append(more); }
  container.append(button('Refresh questions', render)); return container;
}


async function channelsView() {
  const generation = state.generation;
  const scopePath = path('');
  const channels = await api(scopePath + '/channels');
  const channel = channels.find(c => c.id === state.channelId) || channels[0];
  if (channel?.id !== state.channelId) state.threadId = '';
  state.channelId = channel?.id || '';
  const create = () => modal('Create a channel', close => form([
    field('Channel name', { name: 'name', required: true, maxlength: 64, pattern: '[a-z0-9][a-z0-9_-]*', placeholder: 'project-updates' }).node,
    field('Description', { name: 'description', maxlength: 2000 }).node,
    el('p', { class: 'help' }, 'All workspace members and connected sessions can read this channel.')
  ], 'Create channel', async data => {
    const created = await api(scopePath + '/channels', { method: 'POST', body: { name: data.get('name'), description: data.get('description') } });
    state.channelId = created.id; state.threadId = ''; state.channelSearch = ''; close(); await render();
  }));
  if (!channel) return el('section', { class: 'card empty' }, el('h2', {}, 'Give a discussion its own space.'), el('p', {}, 'Create a channel for a project or topic. Sessions can subscribe to receive new posts in their inbox.'), button('Create a channel', create, 'primary'));
  const select = el('select', { 'aria-label': 'Channel', onchange: e => { state.channelId = e.target.value; state.threadId = ''; state.channelSearch = ''; action(null, render); } }, channels.map(c => el('option', { value: c.id, selected: c.id === channel.id }, '#' + c.name)));
  const search = form([field('Search channel', { name: 'query', type: 'search', value: state.channelSearch, maxlength: 256 }).node], 'Search posts', async data => { state.channelSearch = data.get('query').trim(); state.threadId = ''; await render(); });
  const container = el('div', {}, el('section', { class: 'card' }, el('div', { class: 'actions' }, el('label', {}, 'Channel', select), button('New channel', create)), el('p', {}, channel.description), el('p', { class: 'help' }, `${channel.subscribers} subscribed sessions. Ask a connected session to join #${channel.name} using subscribe_channel to receive future notifications.`), search));
  if (state.threadId) container.append(el('div', { class: 'actions' }, el('h2', {}, 'Thread'), button('Back to channel', async () => { state.threadId = ''; await render(); })));
  const query = new URLSearchParams({ channelId: channel.id });
  if (state.threadId) query.set('threadId', state.threadId);
  if (state.channelSearch) query.set('query', state.channelSearch);
  const [page, members, directory] = await Promise.all([api(scopePath + '/channel-posts?' + query), api(scopePath + '/members'), api(scopePath + '/sessions')]);
  const names = new Map(members.map(m => ['human:' + m.userId, m.email]));
  for (const session of directory.sessions) names.set('agent:' + session.id, session.displayName);
  const list = el('div', { class: 'conversation-list', 'aria-label': 'Channel posts' });
  const rendered = new Map();
  const append = (page, fresh = false) => {
    const posts = fresh ? [...page.posts].reverse() : page.posts;
    for (const post of posts) {
      const fingerprint = JSON.stringify(post), previous = rendered.get(post.id);
      if (previous?.fingerprint === fingerprint) continue;
      const item = el('article', { class: 'card' }, el('div', { class: 'row' }, el('strong', {}, names.get(post.authorKind + ':' + post.authorId) || post.authorId), el('small', {}, date(post.createdAt))), el('p', { class: 'message' }, post.body));
      item.append(attachmentLinks(scopePath, post.body));
      if (!state.threadId || state.channelSearch) item.append(button(post.replies ? `${post.replies} ${post.replies === 1 ? 'reply' : 'replies'}` : 'Open thread', async () => { state.threadId = post.threadId || post.id; state.channelSearch = ''; await render(); }));
      if (previous) previous.node.replaceWith(item); else if (fresh) list.prepend(item); else list.append(item);
      rendered.set(post.id, { fingerprint, node: item });
    }
  };
  append(page);
  if (!page.posts.length) list.append(el('section', { class: 'card empty' }, el('p', {}, state.channelSearch ? 'No matching posts.' : 'No posts yet. Start the discussion below.')));
  container.append(list);
  if (page.nextCursor) { let cursor = page.nextCursor; const more = button('Earlier posts', async () => { const nextQuery = new URLSearchParams(query); nextQuery.set('before', cursor); const older = await api(scopePath + '/channel-posts?' + nextQuery); append(older); cursor = older.nextCursor; if (!cursor) more.remove(); }); container.append(more); }
  const body = field(state.threadId ? 'Your reply' : 'Your post', { name: 'body', multiline: true, required: true, maxlength: 65536 });
  let key = crypto.randomUUID(), draft = '';
  container.append(el('section', { class: 'card' }, form([body.node, attachmentPicker(scopePath, body.input), el('p', { class: 'help' }, 'Shared with the workspace. Subscribed sessions receive an inbox notification; delivery does not mean a model has acted.')], state.threadId ? 'Reply in thread' : 'Post to channel', async data => {
    const text = data.get('body'); if (draft && draft !== text) key = crypto.randomUUID(); draft = text;
    await api(scopePath + '/channel-posts', { method: 'POST', body: { channelId: channel.id, body: text, threadId: state.threadId || undefined, idempotencyKey: key } });
    state.channelSearch = ''; await render();
  })));
  let live = true, polling = false;
  const timer = setInterval(async () => { if (state.generation !== generation) { clearInterval(timer); return; }
    if (!live || polling || document.hidden) return; polling = true; try { const next = await api(scopePath + '/channel-posts?' + query); if (live) { if (next.posts.length) list.querySelector('.empty')?.remove(); append(next, true); } } catch (error) { if (live) inform('Channel refresh failed. ' + error.message, true); } finally { polling = false; } }, 5000);
  viewCleanups.push(() => { live = false; clearInterval(timer); });
  return container;
}


async function billingView() {
  const scopePath = path('/billing');
  const summary = await api(scopePath);
  const paid = summary.paidUntil && Date.parse(summary.paidUntil) > Date.now();
  const trial = !paid && Date.parse(summary.trialEndsAt) > Date.now();
  const money = cents => new Intl.NumberFormat(undefined, { style: 'currency', currency: summary.currency.toUpperCase(), maximumFractionDigits: 0 }).format(cents / 100);
  const bytes = n => n >= 1024 ** 3 ? `${(n / 1024 ** 3).toFixed(1)} GiB` : `${(n / 1024 ** 2).toFixed(1)} MiB`;
  const count = n => Number(n).toLocaleString();
  const result = el('div');
  if (summary.configured && !summary.live) result.append(el('p', { class: 'notice-inline', role: 'status' }, 'Test billing — this service is not accepting real payments.'));
  if (params.has('billing')) result.append(el('p', { class: 'notice-inline' }, 'Returned from billing. Check payment status below; returning from checkout does not confirm a payment.'));
  result.append(el('section', { class: 'card' }, el('h2', {}, paid ? 'Paid workspace' : trial ? 'Workspace trial' : 'Hosting inactive'),
    el('p', {}, paid ? `Hosting paid through ${date(summary.paidUntil)}.` : trial ? `Trial ends ${date(summary.trialEndsAt)}.` : 'New work is paused. Existing history remains available.'),
    summary.cancelAtPeriodEnd ? el('p', {}, `Your subscription will end on ${date(summary.currentPeriodEnd)}. It will not renew.`) : null,
    ['past_due', 'unpaid', 'incomplete'].includes(summary.status) ? el('p', { class: 'notice-inline' }, 'Your subscription needs payment attention. Open payment settings to review it.') : null));
  const controls = el('div', { class: 'actions' });
  async function follow(action, body) {
    const link = await api(scopePath + '/' + action, { method: 'POST', body, timeout: 30000 });
    const target = new URL(link.url);
    if (target.protocol !== 'https:' || target.username || target.password || !['checkout.stripe.com', 'billing.stripe.com'].includes(target.host)) throw new Error('Payment destination could not be verified.');
    location.assign(target.href);
  }
  if (summary.canManage && summary.configured) {
    if (['none', 'canceled', 'incomplete_expired'].includes(summary.status)) {
      controls.append(button(`${money(summary.monthlyPriceCents)} / month`, () => follow('checkout', { interval: 'month' }), 'primary'), button(`${money(summary.annualPriceCents)} / year`, () => follow('checkout', { interval: 'year' })));
    } else controls.append(button('Payment settings & cancellation', () => follow('portal'), 'primary'));
    controls.append(button('Check payment status', async () => { await api(scopePath + '/refresh', { method: 'POST', timeout: 30000 }); await render(); inform('Payment status checked.'); }));
    if (summary.pendingCheckout) controls.append(button('Close unfinished checkout', async () => { await api(scopePath + '/cancel-checkout', { method: 'POST', timeout: 30000 }); await render(); }));
  }
  result.append(el('section', { class: 'card' }, el('h2', {}, `${money(summary.monthlyPriceCents)} per workspace per month`),
    el('p', {}, `Or ${money(summary.annualPriceCents)} per year, billed annually. Applicable tax is shown at checkout. Includes 5 people, 50 concurrent sessions, 100,000 new deliveries per calendar month and 5 GiB of files.`),
    el('p', { class: 'help' }, 'Includes 25 GiB of uploads and 25 GiB of downloads per calendar month. Each file can be up to 16 MiB. Model and compute costs are separate. No automatic usage overage charges.'),
    summary.pendingCheckout ? el('p', { class: 'notice-inline' }, `An unfinished ${summary.pendingInterval === 'year' ? 'annual' : 'monthly'} checkout is saved. Choosing the same plan resumes it; choosing the other plan closes it before starting another.`) : null,
    !summary.configured ? el('p', {}, 'Paid hosting is not available on this service yet.') : !summary.canManage ? el('p', {}, 'Only a workspace owner can manage payments.') : controls));
  const usage = el('section', { class: 'card' }, el('h2', {}, 'Included usage'), el('p', { class: 'help' }, paid ? `Delivery and file-transfer counters reset each calendar month (UTC). Current month: ${summary.month}.` : 'The trial includes 2,000 new deliveries in total, 256 MiB of files and 1 GiB in each transfer direction per calendar month.'));
  for (const [name, used, limit, format] of [['New deliveries', summary.deliveries, summary.deliveryLimit, count], ['People', summary.members, summary.memberLimit, count], ['Concurrent sessions', summary.sessions, summary.sessionLimit, count], ['Retained coordination data', summary.retainedBytes || 0, summary.textLimit || 5 * 1024 ** 3, bytes], ['File storage', summary.fileBytes, summary.fileStorageLimit, bytes], ['Uploads this month', summary.uploadedBytes, summary.fileTransferLimit, bytes], ['Downloads this month', summary.downloadedBytes, summary.fileTransferLimit, bytes]]) {
    usage.append(el('div', { class: 'usage' }, el('div', { class: 'row' }, el('span', {}, name), el('span', {}, `${format(used)} / ${format(limit)}`)), el('progress', { max: limit, value: Math.min(used, limit), 'aria-label': name })));
  }
  usage.append(el('p', { class: 'help' }, 'A new message counts once per recipient. Channel notifications count per subscriber. Retries, acknowledgements and replies do not consume delivery allowance.'));
  result.append(usage);
  return result;
}


async function dataView() {
  const scopePath = path('');
  const workspace = state.workspace;
  const storage = await api(scopePath + '/storage');
  const owner = state.workspace.role === 'owner';
  const result = el('div', {}, el('section', { class: 'card' }, el('h2', {}, 'Retention'), el('p', {}, 'Completed conversations, completed tasks, resolved questions and quiet channel threads are eligible for removal after 90 days. Unfinished work and linked records that still need each other remain. Files stay until you delete them.'), el('p', { class: 'help' }, 'The notification log keeps roughly the latest 100,000 events or 90 days, whichever is shorter. This does not delete the corresponding messages or tasks. New work pauses when estimated retained coordination data reaches 5 GiB; final replies and acknowledgements remain available.')));
  if (!owner) { result.append(el('p', {}, 'A workspace owner can pause sessions, export data and prune completed history.')); return result; }
  if (workspace.deleting) result.append(el('section', { class: 'card' }, el('h2', {}, 'Deletion in progress'), el('p', {}, 'Sessions and purchases are stopped. Billing must be closed before the remaining data can be deleted. The service retries automatically; you can also retry below.')));
  else result.append(el('section', { class: 'card' }, el('h2', {}, state.workspace.paused ? 'Sessions paused' : 'Pause sessions for an export'),
    el('p', {}, 'Pausing disconnects all sessions and prevents new connections while you export. Members can still read history here. Resume and reconnect your apps when finished. Pausing does not cancel billing.'),
    button(state.workspace.paused ? 'Resume sessions' : 'Pause sessions', () => modal(state.workspace.paused ? 'Resume this workspace?' : 'Disconnect workspace sessions?', close => el('div', {}, el('p', {}, state.workspace.paused ? 'Connected apps can rejoin when hosting is active.' : 'Every connected session will lose its current connection. Queued work and history are retained.'), button('Confirm', async () => { await api(scopePath + '/pause', { method: 'POST', body: { paused: !state.workspace.paused } }); close(); await signedIn(); }, 'primary'))))));
  result.append(el('section', { class: 'card' }, el('h2', {}, 'Download a portable archive'),
    el('p', {}, 'Includes retained messages, tasks, questions, channels, output values and file bytes. Does not contain account credentials or payment details. Pause sessions first so the archive is consistent.'),
    el('p', { class: 'help' }, 'Portable archives are limited to 64 MiB, including encoded files. Larger workspaces need an operator-assisted database and file export. File bytes in an archive count toward download allowance. Store the download privately.'),
    button('Download workspace archive', async () => { const response = await fileRequest(scopePath + '/export'); const blob = await response.blob(); const url = URL.createObjectURL(blob); const link = el('a', { href: url, download: 'agentintercom-workspace.json' }); document.body.append(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(url), 30000); })));
  const cutoff = field('Remove eligible history completed before', { name: 'before', type: 'date', required: true, value: new Date(Date.now() - 90 * 86400000).toISOString().slice(0, 10), max: new Date(Date.now() - 2 * 86400000).toISOString().slice(0, 10) });
  result.append(el('section', { class: 'card' }, el('h2', {}, 'Prune completed history'), el('p', {}, 'Review a preview before deleting anything. Export first if you need to retain a copy. This does not remove unfinished work, files or your subscription.'),
    form([cutoff.node], 'Preview cleanup', async data => {
      const before = new Date(data.get('before') + 'T00:00:00Z').toISOString();
      const preview = await api(scopePath + '/prune', { method: 'POST', body: { before, execute: false }, timeout: 30000 });
      modal('Delete eligible history?', close => el('div', {}, el('p', {}, 'This permanently deletes eligible records before the selected date. Counts may change if work finishes before confirmation.'), el('pre', {}, JSON.stringify(preview.records, null, 2)), button('Delete eligible history', async () => { await api(scopePath + '/prune', { method: 'POST', body: { before, execute: true }, timeout: 30000 }); close(); await render(); inform('Completed history pruned.'); }, 'danger')));
    })));
  result.append(el('section', { class: 'card' }, el('h2', {}, 'Retained records'), el('p', { class: 'help' }, 'Payload estimates below exclude database indexes and some record overhead; billing usage includes a per-record estimate.'), (storage.records || []).map(record => el('div', { class: 'row' }, el('span', {}, `${record.recordType} · ${record.state}`), el('span', {}, Number(record.count).toLocaleString())))));
  result.append(el('section', { class: 'card' }, el('h2', {}, 'Delete workspace'),
    el('p', {}, 'Permanently removes this workspace for everyone, including messages, unfinished work, files and connections. Its hosting subscription ends immediately; unused time is not automatically refunded. Export first if you need a copy.'),
    el('p', { class: 'help' }, 'Your October account and other workspaces remain. Minimal payment references are retained for support. Private backups expire on the operator’s retention schedule.'),
    button(workspace.deleting ? 'Retry workspace deletion' : 'Delete workspace', () => modal('Permanently delete this workspace?', close => {
      const confirmation = field('Type the workspace name to confirm', { name: 'confirmation', required: true, autocomplete: 'off' });
      return el('div', {}, el('p', {}, `This cannot be undone. Enter ${workspace.name} to confirm. Once requested, deletion continues even if this page closes.`),
        form([confirmation.node], 'Delete permanently', async data => {
          if (data.get('confirmation') !== workspace.name) throw new Error('The workspace name does not match.');
          try { await api(scopePath, { method: 'DELETE', body: { confirmName: data.get('confirmation') }, timeout: 30000 }); }
          catch (error) { close(); await signedIn(); throw error; }
          close(); await signedIn(); inform('Workspace deleted.');
        }));
    }), 'danger')));
  return result;
}
