import { AuthClient } from '@supabase/auth-js';

const root = document.querySelector('#app');
const notice = document.querySelector('#notice');
const state = { auth: null, config: null, user: null, workspaces: [], workspace: null, tab: 'sessions', generation: 0 };
let noticeTimer;
const params = new URLSearchParams(location.search);
const pendingKey = 'october-bus-pending';
let pending = {};
try { const saved = JSON.parse(sessionStorage.getItem(pendingKey) || '{}'); if (saved.until > Date.now()) pending = saved; } catch {}
if (params.get('authorization_id')) pending.authorization = params.get('authorization_id');
const invite = new URLSearchParams(location.hash.slice(1)).get('invite');
if (invite) { pending.invite = invite; history.replaceState(null, '', location.pathname + location.search); }
if (pending.authorization || pending.invite) sessionStorage.setItem(pendingKey, JSON.stringify({ ...pending, until: Date.now() + 20 * 60 * 1000 }));

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
  state.user = null;
  const email = field('Email address', { type: 'email', name: 'email', autocomplete: 'email', required: true });
  const emailForm = form([email.node], 'Continue with email', async data => {
    const { error } = await state.auth.signInWithOtp({ email: data.get('email'), options: { emailRedirectTo: location.origin + '/app' + (pending.authorization ? '?authorization_id=' + encodeURIComponent(pending.authorization) : ''), shouldCreateUser: true } });
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
function invitation() {
  root.replaceChildren(el('main', { id: 'main', class: 'consent' }, brand(), el('section', { class: 'card' }, el('h1', {}, 'Join a workspace'), el('p', {}, `You are signed in as ${state.user.email}. The invitation must match this address.`), el('div', { class: 'actions' }, button('Accept invitation', async () => {
    const result = await api('/api/invites/accept', { method: 'POST', body: { token: pending.invite } });
    localStorage.setItem('october-bus-workspace', result.workspaceId); closePending('invite'); await signedIn();
  }, 'primary'), button('Use another account', signOut), button('Cancel', async () => { closePending('invite'); await signedIn(); })))));
}

const titles = { sessions: ['Sessions', 'Choose the connected session you want to work with.'], connect: ['Connect an app', 'Use your favorite AI app, on this machine or another one.'], members: ['Members & devices', 'Give each person and machine their own access.'] };
async function render() {
  if (!state.workspace) return firstWorkspace();
  const generation = ++state.generation;
  const main = el('main', { id: 'main', class: 'content' });
  const tabs = el('nav', { 'aria-label': 'Workspace' }, Object.entries(titles).map(([key, [title]]) => button(title, async () => { state.tab = key; await render(); }, key === state.tab ? 'selected' : '')));
  [...tabs.children].forEach((node, i) => { if (Object.keys(titles)[i] === state.tab) node.setAttribute('aria-current', 'page'); });
  root.replaceChildren(el('div', { class: 'shell' }, el('aside', { class: 'sidebar' }, brand(), el('div', { class: 'workspace-switch' }, workspaceSelect(render), button('+ New workspace', () => modal('New workspace', close => createWorkspace(async () => { close(); await render(); })), 'subtle-button')), tabs, el('div', { class: 'account' }, el('p', {}, state.user.email), button('Sign out', signOut, 'subtle-button'))), main));
  main.append(el('header', { class: 'topline' }, el('div', {}, el('div', { class: 'eyebrow' }, state.workspace.name), el('h1', {}, titles[state.tab][0]), el('p', { class: 'muted' }, titles[state.tab][1])), state.tab === 'sessions' ? button('Connect an app', async () => { state.tab = 'connect'; await render(); }, 'primary') : null));
  const loading = el('p', { class: 'progress' }, 'Loading…'); main.append(loading);
  try {
    const view = await ({ sessions: sessionsView, connect: connectView, members: membersView })[state.tab]();
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
      list.append(el('article', { class: 'card' }, el('div', { class: 'actions' }, el('h2', {}, session.displayName || session.agentId || session.id), el('span', { class: `badge ${online ? 'live' : ''}` }, online ? 'Connected' : 'Offline')), el('p', { class: 'help' }, session.deviceName), el('p', { class: 'help' }, session.deliveryMode === 'poll' ? 'Inbox available when this app checks for messages. It may need you to resume the conversation.' : 'Wake delivery available'), el('small', {}, `Last seen ${date(session.updatedAt)}`)));
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
    el('section', { class: 'card' }, el('h2', {}, 'Another machine or automation'), el('p', {}, 'Create a separate device credential. Each device can connect its own sessions and can be revoked independently.'), form([field('Device name', { name: 'name', required: true, maxlength: 100, placeholder: 'My laptop' }).node], 'Create device credential', async data => {
      const device = await api(path('/devices'), { method: 'POST', body: { name: data.get('name') } });
      secretDialog('Device credential', device.token, 'Save this in your device’s secret store. It is shown only once. Anyone with it can join sessions to this workspace as you.');
    }), el('p', { class: 'help' }, 'Keep credentials out of chat messages, shell history and source control.')));
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
