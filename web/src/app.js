import { AuthClient } from '@supabase/auth-js';

const root = document.querySelector('#app');
const notice = document.querySelector('#notice');
const state = { auth: null, config: null, user: null, workspaces: [], workspace: null, tab: 'sessions', generation: 0, conversationAgent: '', search: '', channelId: '', threadId: '', channelSearch: '' };
let noticeTimer;
const viewCleanups = [];
function clearView() { for (const cleanup of viewCleanups.splice(0)) cleanup(); }
const params = new URLSearchParams(location.search);
const pendingKey = 'october-bus-pending';
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
const brand = () => el('div', { class: 'brand' }, el('span', { class: 'mark', 'aria-hidden': true }, 'o'), 'october bus');
const date = value => value ? new Date(value).toLocaleString() : '—';
const path = suffix => `/api/workspaces/${encodeURIComponent(state.workspace.id)}${suffix}`;
async function api(url, options = {}) {
  const headers = { 'Content-Type': 'application/json' };
  if (state.auth && url !== '/api/config') {
    const { data, error } = await state.auth.getSession();
    if (error) throw error;
    if (data.session) headers.Authorization = `Bearer ${data.session.access_token}`;
  }
  const response = await fetch(url, { method: options.method || 'GET', credentials: 'omit', headers, body: options.body == null ? undefined : JSON.stringify(options.body), signal: AbortSignal.timeout(15000) });
  const data = await response.json();
  if (!response.ok || data.ok === false || data.error) throw new Error(data.error?.message || 'The request could not be completed. Please try again.');
  return data.result;
}
async function copy(value) { await navigator.clipboard.writeText(value); inform('Copied to clipboard.'); }
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
    el('section', { class: 'card auth-card', 'aria-labelledby': 'signin-title' }, el('h2', { id: 'signin-title' }, 'Sign in to October'), el('p', { class: 'help' }, 'Use your existing October account, or create one with your email.'), emailForm, el('details', {}, el('summary', {}, 'Use an existing password'), passwordForm), el('p', { class: 'help' }, 'Your AI subscriptions stay with your apps. October Bus connects the work.')))));
}

async function signedIn() {
  state.user = await api('/api/me');
  state.workspaces = await api('/api/workspaces');
  const wanted = localStorage.getItem('october-bus-workspace');
  state.workspace = state.workspaces.find(w => w.id === wanted) || state.workspaces[0] || null;
  if (pending.invite) return invitation();
  if (pending.authorization) return consent();
  if (pending.device) return deviceConsent();
  if (!state.workspace) return firstWorkspace();
  await render();
}
function createWorkspace(onCreated) {
  const name = field('Workspace name', { name: 'name', placeholder: 'My team', maxlength: 100, required: true });
  return form([name.node, el('p', { class: 'help' }, 'Start a 14-day trial with up to 5 people. No payment details needed.')], 'Create workspace', async data => {
    const workspace = await api('/api/workspaces', { method: 'POST', body: { name: data.get('name') } });
    state.workspaces.push(workspace); state.workspace = workspace; localStorage.setItem('october-bus-workspace', workspace.id); await onCreated();
  });
}
function firstWorkspace() {
  root.replaceChildren(el('main', { id: 'main', class: 'auth-wrap' }, brand(), el('section', { class: 'card consent' }, el('h1', {}, 'Make room for your team.'), el('p', {}, 'A workspace holds your connected sessions, messages and shared work.'), createWorkspace(render), button('Sign out', signOut))));
}
async function signOut() { const { error } = await state.auth.signOut({ scope: 'local' }); if (error) throw error; state.generation++; signIn(); }
function workspaceSelect(onChange) {
  const select = el('select', { 'aria-label': 'Workspace', onchange: e => { state.workspace = state.workspaces.find(w => w.id === e.target.value); localStorage.setItem('october-bus-workspace', state.workspace.id); action(null, onChange); } }, state.workspaces.map(w => el('option', { value: w.id, selected: w.id === state.workspace?.id }, w.name)));
  return select;
}
async function consent() {
  const request = await api(`/api/oauth/requests/${encodeURIComponent(pending.authorization)}`);
  const approve = async allowed => {
    const result = await api(`/api/oauth/requests/${encodeURIComponent(pending.authorization)}`, { method: 'POST', body: { approve: allowed, workspaceId: allowed ? state.workspace?.id : '' } });
    closePending('authorization'); location.assign(result.redirectUrl);
  };
  const choice = state.workspaces.length ? el('div', {}, el('label', {}, 'Connect to workspace', workspaceSelect(consent)), el('div', { class: 'actions' }, button('Allow connection', () => approve(true), 'primary'), button('Cancel', () => approve(false)))) : createWorkspace(consent);
  root.replaceChildren(el('main', { id: 'main', class: 'consent' }, brand(), el('section', { class: 'card' }, el('div', { class: 'eyebrow' }, 'Connect an app'), el('h1', {}, request.clientName), el('p', {}, 'wants to connect to your October Bus workspace.'), el('p', { class: 'help' }, 'This app registered itself. October has not verified its publisher. Only continue if you started this connection in an app you trust.'), el('ul', { class: 'permission-list' }, el('li', {}, 'Read and send messages in the selected workspace.'), el('li', {}, 'Discover connected sessions and coordinate shared tasks.'), el('li', {}, 'Remain connected until you revoke access or the connection expires.')), el('p', { class: 'help' }, 'You can revoke access at any time in Members & devices.'), el('p', { class: 'help' }, 'Returns to: ', el('code', {}, new URL(request.redirectUri).origin)), choice)));
}
async function deviceConsent() {
  const request = await api(`/api/device/authorizations/${encodeURIComponent(pending.device)}`);
  const approve = async allowed => {
    await api(`/api/device/authorizations/${encodeURIComponent(pending.device)}`, { method: 'POST', body: { approve: allowed, workspaceId: allowed ? state.workspace?.id : '' } });
    closePending('device');
    root.replaceChildren(el('main', { id: 'main', class: 'consent' }, brand(), el('section', { class: 'card' }, el('h1', {}, allowed ? 'Your device can connect.' : 'Connection declined.'), el('p', {}, allowed ? 'Return to your terminal to finish. Each session on this device will appear in your workspace.' : 'No device credential was issued.'), button('Open workspace', signedIn, 'primary'))));
  };
  const choice = state.workspaces.length ? el('div', {}, el('label', {}, 'Connect to workspace', workspaceSelect(deviceConsent)), el('div', { class: 'actions' }, button('Connect this device', () => approve(true), 'primary'), button('Decline', () => approve(false)))) : createWorkspace(deviceConsent);
  root.replaceChildren(el('main', { id: 'main', class: 'consent' }, brand(), el('section', { class: 'card' }, el('div', { class: 'eyebrow' }, 'Connect a machine'), el('h1', {}, request.name), el('p', {}, 'Check that this code matches the one in your terminal:'), el('pre', {}, request.userCode), el('p', { class: 'help' }, 'Only approve a request you started. This device can read and send workspace messages and start its own agent sessions. You can revoke it in Members & devices.'), choice)));
}
function invitation() {
  root.replaceChildren(el('main', { id: 'main', class: 'consent' }, brand(), el('section', { class: 'card' }, el('h1', {}, 'Join a workspace'), el('p', {}, `You are signed in as ${state.user.email}. The invitation must match this address.`), el('div', { class: 'actions' }, button('Accept invitation', async () => {
    const result = await api('/api/invites/accept', { method: 'POST', body: { token: pending.invite } });
    localStorage.setItem('october-bus-workspace', result.workspaceId); closePending('invite'); await signedIn();
  }, 'primary'), button('Use another account', signOut), button('Cancel', async () => { closePending('invite'); await signedIn(); })))));
}

const titles = { channels: ['Channels', 'Shared discussions and threads for this workspace.'], messages: ['Conversations', 'Messages and receipts shared with this workspace.'], tasks: ['Tasks', 'Shared work, claimed by the session doing it.'], questions: ['Needs you', 'Help your agents continue when they need a decision.'], sessions: ['Sessions', 'Choose the connected session you want to work with.'], connect: ['Connect an app', 'Use your favorite AI app, on this machine or another one.'], members: ['Members & devices', 'Give each person and machine their own access.'] };
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
    const view = await ({ channels: channelsView, messages: messagesView, tasks: tasksView, questions: questionsView, sessions: sessionsView, connect: connectView, members: membersView })[state.tab]();
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
    }
  };
  if (!page.sessions.length) return el('section', { class: 'card empty' }, el('h2', {}, 'Your first connection starts here.'), el('p', { class: 'muted' }, 'Connect an AI app or terminal session. It will appear here under its own name, ready for shared work.'), button('Connect an app', async () => { state.tab = 'connect'; await render(); }, 'primary'));
  append(page); container.append(list);
  if (page.nextCursor) { let cursor = page.nextCursor; const more = button('Show more sessions', async () => { const next = await api(path('/sessions') + '?after=' + encodeURIComponent(cursor)); append(next); cursor = next.nextCursor; if (!cursor) more.remove(); }); container.append(more); }
  container.append(el('p', { class: 'help' }, 'Connected means the app contacted October Bus recently. It does not mean the model is currently working.'), button('Refresh sessions', render)); return container;
}
function connectView() {
  const url = state.config.mcpUrl;
  return el('div', { class: 'grid' }, el('section', { class: 'card' }, el('h2', {}, 'An app with remote MCP'), el('p', {}, 'In your app’s connectors or MCP settings, add October Bus using this address.'), el('pre', {}, url), button('Copy connector URL', () => copy(url)), el('ol', {}, el('li', {}, 'Add the connector in your app.'), el('li', {}, 'Sign in to October and choose this workspace.'), el('li', {}, 'Ask your app to check its Bus inbox or find connected sessions.')), el('p', { class: 'help' }, 'Works with compatible Streamable HTTP MCP clients. Your app controls which conversations can use tools and whether an idle conversation can resume.')),
    el('section', { class: 'card' }, el('h2', {}, 'Another machine or automation'), el('p', {}, 'Install the October Bus CLI on that machine, then run:'), el('pre', {}, 'october-bus hosted login --server ' + location.origin), el('p', {}, 'Open the link it prints, confirm the code and choose this workspace. Then run october-bus connect --hosted default, or october-bus session start claude --hosted default.'), el('details', {}, el('summary', {}, 'Create a credential for custom automation'), form([field('Device name', { name: 'name', required: true, maxlength: 100, placeholder: 'My laptop' }).node], 'Create device credential', async data => {
      const device = await api(path('/devices'), { method: 'POST', body: { name: data.get('name') } });
      secretDialog('Device credential', device.token, 'Save this in your device’s secret store. It is shown only once. Anyone with it can join sessions to this workspace as you.');
    }), el('p', { class: 'help' }, 'Keep credentials out of chat messages, shell history and source control.'))));
}
async function membersView() {
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
    deviceList.append(el('div', { class: 'row' }, el('div', { class: 'details' }, el('strong', {}, device.name), el('p', { class: 'help' }, `${device.kind === 'mcp' ? 'MCP app' : 'Device'} · ${revoked ? 'Revoked' : `Expires ${date(device.expiresAt)}`}`)), !revoked && (canManage || device.userId === state.user.id) ? button('Revoke', () => modal('Revoke connection?', close => el('div', {}, el('p', {}, `${device.name} will stop receiving access to this workspace.`), button('Revoke connection', async () => { await api(path(`/devices/${encodeURIComponent(device.id)}`), { method: 'DELETE' }); close(); await render(); }, 'danger'))), 'danger') : null));
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
  state.auth = new AuthClient({ url: state.config.authUrl + '/auth/v1', headers: { apikey: state.config.authPublicKey, Authorization: `Bearer ${state.config.authPublicKey}` }, storageKey: 'october-bus-auth', flowType: 'pkce', autoRefreshToken: true, persistSession: true, detectSessionInUrl: true });
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
    const compose = form([el('label', {}, 'Send to', target), body.node, el('p', { class: 'help' }, 'Visible to workspace members. An MCP app may need you to resume it or ask it to check its inbox.')], 'Send request', async data => {
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
  container.append(el('section', { class: 'card' }, form([body.node, el('p', { class: 'help' }, 'Shared with the workspace. Subscribed sessions receive an inbox notification; delivery does not mean a model has acted.')], state.threadId ? 'Reply in thread' : 'Post to channel', async data => {
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
