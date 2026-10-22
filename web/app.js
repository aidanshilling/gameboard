// GameBoard frontend: a plain ES module, no build step.

// Colours a board column can have (must match the server's list).
const COLORS = ['periwinkle', 'peach', 'ice', 'mint', 'lilac', 'sun', 'rose', 'stone'];

const LEVELS = [
  { level: 2, label: 'Want to play', cls: 'want' },
  { level: 1, label: 'Maybe', cls: 'maybe' },
  { level: -1, label: 'Pass', cls: 'pass' },
];

const SORTS = [
  ['manual', 'Manual order'],
  ['wanted', 'Most wanted'],
  ['name', 'Name'],
  ['price', 'Price'],
  ['added', 'Recently added'],
];

const state = {
  user: null,
  board: null,
  boardEtag: null, // tag of the board data held, for "has it changed?" checks
  columns: [],
  games: [],
  boards: null,
  filter: '',
  sort: load('sort') || 'manual',
  pollTimer: null,
  dragging: false,
  modal: null,
  noteDraft: '',
  manageOpen: false,
  adminSecret: null, // { title, username, password } shown once on the admin page
  onModalClose: null, // called once when the open dialog closes
  ticker: null, // footer ticker: { items, mine, resetsAt, maxLength, perUser }
  tickerAt: 0,
  // Chat feed. Side and hidden-ness are per-browser preferences.
  feedSide: load('feedSide') === 'left' ? 'left' : 'right',
  feedHidden: load('feedHidden') === '1',
  feedMobileOpen: false,
  avatars: {}, // user id -> profile picture version
  avatarsAt: 0,
  feedTab: load('feedTab') === 'site' ? 'site' : 'board', // which chat a board page shows
  channels: {}, // chat channels by key ('site', 'board:<id>'): posts, draft, linked game…
  pendingGame: null, // game to open once its board has loaded (from a chat link)
  playlists: null, // everyone's playlists (summaries), as last loaded
  onPlaylistChange: null, // the page's hook for when a song is added to a playlist
  currentPage: null, // draws the page that's showing (again, with newer data)
};

const $app = document.getElementById('app');

// ---------- helpers ----------

function load(key) {
  try { return localStorage.getItem('gb:' + key); } catch { return null; }
}
function store(key, value) {
  try { localStorage.setItem('gb:' + key, value); } catch { /* storage unavailable */ }
}

// h builds DOM nodes. Strings always become text nodes, so user content can't inject HTML.
function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'dataset') Object.assign(el.dataset, v);
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else if (k === 'value' || k === 'checked' || k === 'disabled') el[k] = v;
    else el.setAttribute(k, v === true ? '' : String(v));
  }
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false || c === '') continue;
    el.append(c instanceof Node ? c : String(c));
  }
  return el;
}

// win wraps content in a classic-Mac style window: a striped title bar with a
// close box (a working one if onClose is given) and a body.
function win(title, opts, ...body) {
  const { cls = '', onClose = null, extra = null } = opts || {};
  return h('section', { class: 'win ' + cls },
    h('div', { class: 'win-bar' },
      onClose
        ? h('button', { class: 'win-close', type: 'button', onclick: onClose, 'aria-label': 'Close', title: 'Close' })
        : h('span', { class: 'win-close', 'aria-hidden': 'true' }),
      h('span', { class: 'win-title' }, title),
      extra),
    h('div', { class: 'win-body' }, ...body));
}

async function api(method, path, body) {
  const opts = { method, headers: { 'X-Requested-With': 'gameboard' }, credentials: 'same-origin' };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch('/api' + path, opts);
  if (method !== 'GET') forgetPages();
  const text = await res.text();
  let data = null;
  if (text) {
    try { data = JSON.parse(text); } catch { /* not JSON */ }
  }
  if (res.status === 401 && state.user) {
    // Session expired mid-use: go back to the login screen.
    state.user = null;
    setTimeout(route);
  }
  if (!res.ok) throw new Error((data && data.error) || `${res.status} ${res.statusText}`);
  return data;
}

// apiPoll GETs something the page re-checks regularly. It sends the tag of
// the version the page already has (etag); if nothing has changed the server
// answers 304 with no body, and this returns null. Otherwise it returns
// { data, etag }.
async function apiPoll(path, etag) {
  const headers = { 'X-Requested-With': 'gameboard' };
  if (etag) headers['If-None-Match'] = etag;
  const res = await fetch('/api' + path, { headers, credentials: 'same-origin' });
  if (res.status === 304) return null;
  const data = await res.json().catch(() => null);
  if (res.status === 401 && state.user) {
    state.user = null;
    setTimeout(route);
  }
  if (!res.ok) throw new Error((data && data.error) || `${res.status} ${res.statusText}`);
  return { data, etag: res.headers.get('ETag') };
}

let toastTimer;
function toast(msg, kind = 'error') {
  const t = document.getElementById('toast');
  t.textContent = msg;
  t.className = 'toast ' + kind;
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, 3500);
}

function ago(ts) {
  const s = Math.floor(Date.now() / 1000) - ts;
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  if (s < 86400 * 30) return Math.floor(s / 86400) + 'd ago';
  return new Date(ts * 1000).toLocaleDateString();
}

function safeUrl(u) {
  return typeof u === 'string' && /^https?:\/\//i.test(u) ? u : null;
}

function cap(s) { return s ? s[0].toUpperCase() + s.slice(1) : s; }

function initials(name) { return (name || '?').slice(0, 2).toUpperCase(); }

// Profile pictures. state.avatars maps user id -> { v, pixel } (from
// /api/users): the version in the URL lets browsers cache each picture, and
// pixel art is drawn unsmoothed so it stays crisp.
function avatarUrl(userId) {
  const a = state.avatars[userId];
  return a ? `/api/avatars/${userId}?v=${a.v}` : null;
}

// avatar draws someone's picture, or their initials if they haven't set one.
function avatar(userId, name, cls = '', title = null) {
  const url = avatarUrl(userId);
  const pixel = url && state.avatars[userId].pixel;
  return h('span', { class: `dot ${cls}${url ? ' has-img' : ''}`, title },
    url ? h('img', { src: url, alt: '', loading: 'lazy', class: pixel ? 'pixel' : null }) : initials(name));
}

async function loadAvatars() {
  try {
    const users = await api('GET', '/users');
    const avatars = Object.fromEntries(users.filter(u => u.avatar).map(u => [u.id, { v: u.avatar, pixel: !!u.avatarPixel }]));
    const changed = JSON.stringify(avatars) !== JSON.stringify(state.avatars);
    state.avatars = avatars;
    state.avatarsAt = Date.now();
    if (changed) drawFeed(); // the chat stays put between pages, so update its pictures
  } catch { /* initials it is */ }
}

function levelInfo(level) { return LEVELS.find(l => l.level === level); }

function interestText(i) {
  const l = levelInfo(i.level);
  return `${i.username}: ${l ? l.label.toLowerCase() : 'no vote'}${i.owns ? ', owns it' : ''}`;
}

function interestScore(g) { return g.interests.reduce((s, i) => s + i.level, 0); }

function priceCents(g) {
  const p = g.meta && g.meta.price;
  if (!p) return 1e12;
  return p.free ? 0 : p.cents;
}

// linkify turns http(s) URLs in note text into links, leaving the rest as text.
function linkify(text) {
  const parts = [];
  const re = /https?:\/\/[^\s<>"]+/g;
  let last = 0, m;
  while ((m = re.exec(text))) {
    if (m.index > last) parts.push(text.slice(last, m.index));
    parts.push(h('a', { href: m[0], target: '_blank', rel: 'noopener noreferrer' }, m[0]));
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push(text.slice(last));
  return parts;
}

// ---------- page data cache ----------

// Pages draw straight away from the data they had last time, then check for
// newer data behind the scenes and redraw if it changed. Waiting on the
// server before drawing anything is what made moving between pages feel slow.
const pageCache = new Map(); // GET path -> { data, json }
let routeSeq = 0; // bumps on every page change, so late answers know they're stale

// pageData gets a page's data: the remembered copy if there is one (checking
// for a newer one in the background), otherwise from the server.
async function pageData(path) {
  const seq = routeSeq;
  const cached = pageCache.get(path);
  const fetchFresh = () => api('GET', path).then(data => {
    const json = JSON.stringify(data);
    const changed = !cached || cached.json !== json;
    pageCache.set(path, { data, json });
    return { data, changed };
  });
  if (!cached) return (await fetchFresh()).data;
  // The redraw after a refresh has the newest data already: no need to ask again.
  if (!redrawing) {
    fetchFresh().then(r => { if (r.changed) refreshPage(seq); }).catch(() => { /* keep what's shown */ });
  }
  return cached.data;
}
let redrawing = false;

// Anything that changes data could make remembered pages wrong, so forget
// them; each page fetches fresh on its next visit.
function forgetPages() { pageCache.clear(); }

// refreshPage redraws the current page with newer data, unless the person has
// moved on, has a dialog open, or is typing on the page (their next visit
// shows the update instead).
function refreshPage(seq) {
  if (seq !== routeSeq || state.modal || !state.currentPage) return;
  const el = document.activeElement;
  if (el && el.closest('main') && /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) return;
  if (document.querySelector('main .chip-add, main .music-form')) return; // mid-way through adding something
  const main = document.querySelector('main');
  const top = main ? main.scrollTop : 0;
  redrawing = true; // pages ask for their data straight away, before their first await
  let drawn;
  try { drawn = state.currentPage(); } finally { redrawing = false; }
  Promise.resolve(drawn).then(() => {
    const m = document.querySelector('main');
    if (m) m.scrollTop = top;
  }).catch(() => { /* keep what's shown */ });
}

// Boards are kept per board too (with the tag the server gave them), so going
// back to one draws it at once and then just asks "changed?".
const boardCache = new Map(); // board id -> { board, columns, games, etag }

function stashBoard() {
  if (state.board) boardCache.set(state.board.id, { board: state.board, columns: state.columns, games: state.games, etag: state.boardEtag });
}

// ---------- routing ----------

async function route() {
  routeSeq++;
  stopPolling();
  closeModal();
  stashBoard();
  if (!state.user) {
    try { state.user = await api('GET', '/me'); } catch { state.user = null; }
  }
  if (!state.user) return renderAuth();
  useUserTheme();
  // Keep everyone's picture list fresh enough that new pictures show up,
  // without holding up the page (except the very first time).
  if (!state.avatarsAt) await loadAvatars();
  else if (Date.now() - state.avatarsAt > 60000) loadAvatars();
  const m = location.hash.match(/^#\/board\/(\d+)/);
  const music = location.hash.match(/^#\/music(?:\/(playlists)|\/playlist\/(\d+))?$/);
  state.adminSecret = null;
  state.onPlaylistChange = null;
  let page;
  if (m) page = () => renderBoard(Number(m[1]));
  else if (location.hash === '#/admin') page = renderAdmin;
  else if (music && music[2]) page = () => renderPlaylistPage(Number(music[2]));
  else if (music && music[1]) page = renderPlaylistsPage;
  else if (music) page = renderMusicPage;
  else page = renderBoards;
  state.currentPage = page;
  try {
    await page();
  } catch (e) {
    if (!state.user) return renderAuth();
    shell(h('div', { class: 'empty' }, win('Error', { cls: 'alert' },
      h('p', {}, '⚠ ' + e.message),
      h('a', { href: '#/', class: 'btn' }, 'Back to boards'))));
  }
}

window.addEventListener('hashchange', route);
document.addEventListener('keydown', e => { if (e.key === 'Escape') closeModal(); });

// ---------- auth ----------

async function renderAuth() {
  let setup = { signupsOpen: false, hasAdmin: true };
  try { setup = await api('GET', '/setup'); } catch { /* show login */ }
  let mode = 'login';
  let requested = null; // username of a just-submitted sign-up request

  const card = (...content) => $app.replaceChildren(h('div', { class: 'auth-wrap' },
    win(mode === 'register' ? 'New Account' : 'Welcome', { cls: 'auth-card' },
      h('div', { class: 'brand big' }, h('img', { src: '/logo.png', alt: '' }), 'pingugamer'),
      h('p', { class: 'tagline' }, 'type ron ★ my nevis? ★ your what?'),
      ...content)));

  const draw = () => {
    if (requested) {
      card(
        h('h2', { class: 'center' }, 'Request sent'),
        h('p', { class: 'center' }, `An admin needs to approve "${requested}" before you can log in. You'll use the password you just chose.`),
        h('button', { class: 'btn block', onclick: () => { requested = null; mode = 'login'; draw(); } }, 'Back to log in'));
      return;
    }

    const isReg = mode === 'register';
    const err = h('div', { class: 'form-error', hidden: true });
    const username = h('input', { name: 'username', autocomplete: 'username', required: true, minlength: 3, maxlength: 32, placeholder: 'Username' });
    const password = h('input', { type: 'password', name: 'password', autocomplete: isReg ? 'new-password' : 'current-password', required: true, minlength: isReg ? 8 : null, maxlength: 72, placeholder: isReg ? 'Password (8+ characters)' : 'Password' });
    const confirmPw = isReg ? h('input', { type: 'password', autocomplete: 'new-password', required: true, placeholder: 'Confirm password' }) : null;
    const message = isReg ? h('textarea', { rows: 2, maxlength: 300, placeholder: 'Message for the admin (optional), e.g. who you are' }) : null;
    const submit = h('button', { class: 'btn primary block', type: 'submit' }, isReg ? 'Request account' : 'Log in');

    const form = h('form', {
      class: 'stack',
      onsubmit: async e => {
        e.preventDefault();
        err.hidden = true;
        if (isReg && password.value !== confirmPw.value) {
          err.textContent = 'Passwords do not match';
          err.hidden = false;
          return;
        }
        submit.disabled = true;
        try {
          if (isReg) {
            await api('POST', '/register', { username: username.value, password: password.value, message: message.value });
            requested = username.value.trim();
            draw();
          } else {
            state.user = await api('POST', '/login', { username: username.value, password: password.value });
            route();
          }
        } catch (ex) {
          err.textContent = ex.message;
          err.hidden = false;
          submit.disabled = false;
        }
      },
    }, username, password, confirmPw, message, err, submit);

    const toggle = setup.signupsOpen ? h('p', { class: 'muted center' },
      isReg ? 'Already have an account? ' : 'New here? ',
      h('button', { class: 'link', type: 'button', onclick: () => { mode = isReg ? 'login' : 'register'; draw(); } }, isReg ? 'Log in' : 'Request an account'))
      : null;

    card(
      !setup.hasAdmin ? h('p', { class: 'notice' }, 'No admin account exists yet. Whoever runs the server can create one with ', h('code', {}, 'gameboard create-admin <username>'), '.') : null,
      isReg ? h('p', { class: 'muted xs' }, 'Account requests have to be approved by an admin before you can log in.') : null,
      form,
      toggle);
    username.focus();
  };
  draw();
}

async function logout() {
  try { await api('POST', '/logout'); } catch { /* ignore */ }
  state.user = null;
  state.channels = {};
  state.avatarsAt = 0;
  state.ticker = null;
  state.board = null;
  boardCache.clear(); // the next person to log in here sees their own permissions
  location.hash = '#/';
  route();
}

// ---------- profile picture ----------

const CROP_VIEW = 260; // size of the crop window on screen, in px
const CROP_OUT = 512; // size of the square the browser uploads

// upload sends a file as the raw request body.
async function upload(path, blob) {
  const res = await fetch('/api' + path, {
    method: 'PUT', credentials: 'same-origin', body: blob,
    headers: { 'X-Requested-With': 'gameboard', 'Content-Type': blob.type || 'application/octet-stream' },
  });
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new Error((data && data.error) || `${res.status} ${res.statusText}`);
  return data;
}

// looksLikePixelArt checks a picture for few colours (256 at most), the
// tell-tale of pixel art. Big pictures aren't worth counting.
function looksLikePixelArt(img) {
  if (img.naturalWidth * img.naturalHeight > 2048 * 2048) return false;
  const c = document.createElement('canvas');
  c.width = img.naturalWidth;
  c.height = img.naturalHeight;
  const ctx = c.getContext('2d', { willReadFrequently: true });
  ctx.drawImage(img, 0, 0);
  const px = new Uint32Array(ctx.getImageData(0, 0, c.width, c.height).data.buffer);
  const seen = new Set();
  for (const p of px) {
    seen.add(p);
    if (seen.size > 256) return false;
  }
  return true;
}

// cropToBlob cuts out the part of the picture showing in the crop window
// (offset x, y at the given zoom). Photos come out as a smooth 512px JPEG;
// pixel art comes out at its own pixel size, unsmoothed, as a lossless PNG.
function cropToBlob(img, x, y, scale, pixel) {
  let sx = -x / scale;
  let sy = -y / scale;
  let size = CROP_VIEW / scale;
  let out = CROP_OUT;
  if (pixel) {
    // Snap to whole pixels, and never scale up.
    sx = Math.round(sx);
    sy = Math.round(sy);
    size = Math.max(1, Math.min(Math.round(size), img.naturalWidth - sx, img.naturalHeight - sy));
    out = Math.min(size, CROP_OUT);
  }
  const canvas = document.createElement('canvas');
  canvas.width = canvas.height = out;
  const ctx = canvas.getContext('2d');
  if (pixel) {
    ctx.imageSmoothingEnabled = false;
  } else {
    ctx.fillStyle = '#fff';
    ctx.fillRect(0, 0, out, out);
    ctx.imageSmoothingQuality = 'high';
  }
  ctx.drawImage(img, sx, sy, size, size, 0, 0, out, out);
  return new Promise(resolve => canvas.toBlob(resolve, pixel ? 'image/png' : 'image/jpeg', 0.92));
}

// openProfileModal sets or removes the user's picture. The chosen part of the
// image is cut out here, in the browser, so a huge original never gets
// uploaded; the server then shrinks and re-encodes whatever it receives.
function openProfileModal() {
  const root = h('div', { class: 'stack' });
  const fileInput = h('input', {
    type: 'file', accept: 'image/*', class: 'file-input',
    onchange: () => {
      const file = fileInput.files[0];
      fileInput.value = '';
      if (file) startCrop(file);
    },
  });

  const done = message => {
    closeModal();
    toast(message, 'ok');
    route();
  };
  const remove = async () => {
    if (!confirm('Remove your profile picture?')) return;
    try {
      await api('DELETE', '/me/avatar');
      delete state.avatars[state.user.id];
      done('Picture removed');
    } catch (e) { toast(e.message); }
  };

  const showCurrent = () => {
    const has = !!avatarUrl(state.user.id);
    root.replaceChildren(
      h('div', { class: 'profile-head' },
        avatar(state.user.id, state.user.username, 'none huge'),
        h('div', { class: 'stack' },
          h('strong', {}, state.user.username),
          h('p', { class: 'muted xs' }, 'Your picture shows next to your chat posts, notes and votes. Pick any image and choose the part to use; it gets shrunk down before it\'s stored.'),
          h('div', { class: 'row' },
            h('button', { class: 'btn primary sm', onclick: () => fileInput.click() }, has ? 'Change picture' : 'Choose picture'),
            has ? h('button', { class: 'btn danger sm', onclick: remove }, 'Remove picture') : null))),
      fileInput);
  };

  const startCrop = file => {
    if (!file.type.startsWith('image/')) return toast("That file isn't a picture.");
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => cropStep(img, url);
    img.onerror = () => {
      URL.revokeObjectURL(url);
      toast("Couldn't open that picture here; try a JPG or PNG.");
    };
    img.src = url;
  };

  // cropStep shows the picture in a square window: drag to move, slider or
  // mouse wheel to zoom. Whatever's in the window is what gets saved.
  const cropStep = (img, url) => {
    const nat = { w: img.naturalWidth, h: img.naturalHeight };
    const fit = CROP_VIEW / Math.min(nat.w, nat.h); // smallest zoom that fills the window
    const pixel = looksLikePixelArt(img);
    let scale = fit;
    let x = (CROP_VIEW - nat.w * scale) / 2;
    let y = (CROP_VIEW - nat.h * scale) / 2;
    img.className = 'crop-img' + (pixel ? ' pixel' : '');
    img.alt = '';
    img.draggable = false;
    img.style.width = `${nat.w}px`;
    img.style.height = `${nat.h}px`;

    const apply = () => {
      x = Math.min(0, Math.max(CROP_VIEW - nat.w * scale, x));
      y = Math.min(0, Math.max(CROP_VIEW - nat.h * scale, y));
      img.style.transform = `translate(${x}px, ${y}px) scale(${scale})`;
    };
    // zoomTo keeps the point under (cx, cy) still while zooming.
    const zoomTo = (s, cx = CROP_VIEW / 2, cy = CROP_VIEW / 2) => {
      const ix = (cx - x) / scale;
      const iy = (cy - y) / scale;
      scale = s;
      x = cx - ix * scale;
      y = cy - iy * scale;
      apply();
    };
    const zoom = h('input', {
      type: 'range', min: 1, max: 5, step: 0.01, value: 1, 'aria-label': 'Zoom',
      oninput: e => zoomTo(fit * Number(e.target.value)),
    });

    const frame = h('div', { class: 'crop-frame' }, img, h('div', { class: 'crop-guide' }));
    let drag = null;
    frame.addEventListener('pointerdown', e => {
      e.preventDefault();
      drag = { px: e.clientX, py: e.clientY, x, y };
      frame.setPointerCapture(e.pointerId);
    });
    frame.addEventListener('pointermove', e => {
      if (!drag) return;
      x = drag.x + e.clientX - drag.px;
      y = drag.y + e.clientY - drag.py;
      apply();
    });
    frame.addEventListener('pointerup', () => { drag = null; });
    frame.addEventListener('pointercancel', () => { drag = null; });
    frame.addEventListener('wheel', e => {
      e.preventDefault();
      const z = Math.min(5, Math.max(1, Number(zoom.value) * (e.deltaY < 0 ? 1.1 : 1 / 1.1)));
      zoom.value = z;
      const r = frame.getBoundingClientRect();
      zoomTo(fit * z, e.clientX - r.left, e.clientY - r.top);
    }, { passive: false });
    apply();

    const save = h('button', {
      class: 'btn primary',
      onclick: async () => {
        save.disabled = true;
        save.textContent = 'Saving…';
        try {
          const blob = await cropToBlob(img, x, y, scale, pixel);
          const d = await upload('/me/avatar', blob);
          state.avatars[state.user.id] = { v: d.avatar, pixel: d.pixel };
          URL.revokeObjectURL(url);
          done('Profile picture saved');
        } catch (e) {
          toast(e.message);
          save.disabled = false;
          save.textContent = 'Save picture';
        }
      },
    }, 'Save picture');

    root.replaceChildren(
      h('p', { class: 'muted xs' }, 'Drag the picture to move it and zoom to fit. The square is what everyone will see.'),
      frame,
      h('label', { class: 'row crop-zoom' }, h('span', { 'aria-hidden': 'true' }, '−'), zoom, h('span', { 'aria-hidden': 'true' }, '+')),
      h('div', { class: 'row between' },
        h('button', { class: 'btn ghost', onclick: () => { URL.revokeObjectURL(url); showCurrent(); } }, 'Cancel'),
        save));
  };

  openModal('Profile', root);
  showCurrent();
}

// ---------- themes ----------

// THEME_FIELDS are the colours a theme sets: [key, CSS variable, editor label].
const THEME_FIELDS = [
  ['desk', '--desk', 'Background'],
  ['deskInk', '--desk-ink', 'Text on the background'],
  ['paper', '--paper', 'Windows'],
  ['surface', '--white', 'Cards & boxes'],
  ['ink', '--ink', 'Text & borders'],
  ['muted', '--muted', 'Faded text'],
  ['accent', '--orange', 'Accent (main buttons)'],
  ['accentInk', '--accent-ink', 'Text on the accent'],
  ['beak', '--beak', 'Highlight (badges, shadows)'],
  ['link', '--link', 'Links'],
];
const THEME_COLUMN_FIELDS = ['periwinkle', 'peach', 'ice', 'mint', 'sun', 'lilac', 'rose', 'stone']
  .map(k => [k, `--${k}`, cap(k)]);
const ALL_THEME_FIELDS = [...THEME_FIELDS, ...THEME_COLUMN_FIELDS];

const THEME_PRESETS = {
  penguin: {
    name: 'Penguin',
    vars: {
      desk: '#3f3f74', deskInk: '#f3f2ff', paper: '#f5f6fb', surface: '#ffffff', ink: '#222034', muted: '#5f5d80',
      accent: '#f4a46c', accentInk: '#222034', beak: '#df7126', link: '#3f3f74',
      periwinkle: '#e1e1f4', peach: '#ffe3cd', ice: '#d3e3f6', mint: '#cbeedb', sun: '#ffe6a3', lilac: '#e8daf3', rose: '#f7c6c8', stone: '#e2e3ea',
    },
  },
  poolsuite: {
    name: 'Poolsuite',
    vars: {
      desk: '#f8d3d6', deskInk: '#3b2a2e', paper: '#fcf6ef', surface: '#ffffff', ink: '#1b1718', muted: '#74656a',
      accent: '#a9e1e6', accentInk: '#1b1718', beak: '#e8788a', link: '#1d3fbf',
      periwinkle: '#e4dcf6', peach: '#ffe3cd', ice: '#c4e9ed', mint: '#b9ebbf', sun: '#fff1a6', lilac: '#eddcf3', rose: '#f6bcc5', stone: '#e8e0d8',
    },
  },
  midnight: {
    name: 'Midnight',
    vars: {
      desk: '#0d0e1a', deskInk: '#e4e5f5', paper: '#1b1d2e', surface: '#24273d', ink: '#e4e5f3', muted: '#9a9dbd',
      accent: '#f4a46c', accentInk: '#1b1d2e', beak: '#ff8a3d', link: '#9db2ff',
      periwinkle: '#33365e', peach: '#5a3b2a', ice: '#263f5c', mint: '#22483a', sun: '#574820', lilac: '#432f5a', rose: '#5c2a35', stone: '#33364a',
    },
  },
  terminal: {
    name: 'Terminal',
    vars: {
      desk: '#000a00', deskInk: '#39ff6a', paper: '#021402', surface: '#031c03', ink: '#39ff6a', muted: '#22a847',
      accent: '#39ff6a', accentInk: '#001800', beak: '#00c93a', link: '#9dffb6',
      periwinkle: '#0b2e14', peach: '#2c2a08', ice: '#0a2a2c', mint: '#0e3a17', sun: '#2f2e00', lilac: '#26092c', rose: '#380c14', stone: '#11261a',
    },
  },
  vaporwave: {
    name: 'Vaporwave',
    vars: {
      desk: '#2d0b4e', deskInk: '#ffd6f8', paper: '#fff0fd', surface: '#ffffff', ink: '#2b1240', muted: '#7a5a92',
      accent: '#ff71ce', accentInk: '#2b1240', beak: '#01cdfe', link: '#0086bd',
      periwinkle: '#d9d4ff', peach: '#ffd9b8', ice: '#b8f3ff', mint: '#c2ffe0', sun: '#fff6a8', lilac: '#f2ccff', rose: '#ffc4e3', stone: '#ebe0f2',
    },
  },
  win95: {
    name: 'Windows 95',
    vars: {
      desk: '#008080', deskInk: '#ffffff', paper: '#c0c0c0', surface: '#ffffff', ink: '#000000', muted: '#404040',
      accent: '#000080', accentInk: '#ffffff', beak: '#800080', link: '#0000ee',
      periwinkle: '#c6c6ff', peach: '#ffd8a8', ice: '#a8d8ff', mint: '#a8ffc0', sun: '#ffff80', lilac: '#e0b0ff', rose: '#ffb0b0', stone: '#d4d0c8',
    },
  },
};

const hexToRgb = hex => [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16));
const rgbToHex = rgb => '#' + rgb.map(v => Math.round(Math.max(0, Math.min(255, v))).toString(16).padStart(2, '0')).join('');
// mix blends two colours, t of the first.
const mixHex = (a, b, t) => { const B = hexToRgb(b); return rgbToHex(hexToRgb(a).map((v, i) => v * t + B[i] * (1 - t))); };
const luminance = hex => {
  const [r, g, b] = hexToRgb(hex).map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};

// applyTheme sets the page's colours, filling any gaps from the default and
// working out the few colours that are derived from the others. Changing
// them restyles the whole page, so it does nothing if they're already set,
// and returns whether it changed anything.
let appliedTheme = null;
function applyTheme(vars) {
  const key = JSON.stringify(vars);
  if (key === appliedTheme) return false;
  appliedTheme = key;
  const t = { ...THEME_PRESETS.penguin.vars, ...vars };
  const root = document.documentElement;
  for (const [key, cssVar] of ALL_THEME_FIELDS) root.style.setProperty(cssVar, t[key]);
  const [r, g, b] = hexToRgb(t.deskInk);
  root.style.setProperty('--desk-dot', `rgb(${r} ${g} ${b} / 0.07)`);
  root.style.setProperty('--desk-muted', mixHex(t.deskInk, t.desk, 0.72));
  root.style.setProperty('--orange-hi', mixHex(t.accent, t.surface, 0.7));
  const dark = luminance(t.paper) < 0.3;
  root.style.setProperty('--danger-ink', dark ? '#ffb8bd' : '#7d0f1c');
  root.style.colorScheme = dark ? 'dark' : 'light';
  return true;
}

// userThemeVars is the colours of the theme the user has picked.
function userThemeVars(u) {
  if (u && u.theme && u.theme.startsWith('custom:') && u.themeVars) return u.themeVars;
  const preset = u && u.theme && u.theme.startsWith('preset:') ? THEME_PRESETS[u.theme.slice(7)] : null;
  return (preset || THEME_PRESETS.penguin).vars;
}

// useUserTheme applies the user's theme and remembers it in this browser, so
// the next visit starts in the right colours before the server has answered.
function useUserTheme() {
  const vars = userThemeVars(state.user);
  if (applyTheme(vars)) store('themeVars', JSON.stringify(vars));
}

try {
  const cached = JSON.parse(load('themeVars') || 'null');
  if (cached) applyTheme(cached);
} catch { /* default colours */ }

// themeSwatch is a little picture of a theme: a window on its background.
function themeSwatch(vars) {
  const t = { ...THEME_PRESETS.penguin.vars, ...vars };
  const bar = h('span', { class: 'sw-bar' });
  const btn = h('span', { class: 'sw-btn' });
  const text = h('span', { class: 'sw-text' });
  const win = h('span', { class: 'sw-win' }, bar, text, btn);
  const sw = h('span', { class: 'theme-swatch' }, win);
  sw.style.background = t.desk;
  win.style.background = t.paper;
  win.style.borderColor = t.ink;
  bar.style.background = `repeating-linear-gradient(${t.ink} 0 1px, transparent 1px 2px)`;
  text.style.background = t.muted;
  btn.style.background = t.accent;
  btn.style.borderColor = t.ink;
  return sw;
}

// openThemesModal lets people pick a preset, their own or a shared theme,
// and make or edit their own. Editing previews live on the whole page.
async function openThemesModal() {
  const root = h('div', { class: 'stack' });
  let data = { mine: [], shared: [] };
  const reload = async () => {
    try { data = await api('GET', '/themes'); } catch (e) { toast(e.message); }
  };
  const current = () => state.user.theme || 'preset:penguin';
  const choose = async key => {
    try {
      state.user = await api('PUT', '/me/theme', { theme: key });
      useUserTheme();
      draw();
    } catch (e) { toast(e.message); }
  };
  const del = async t => {
    if (!confirm(`Delete the theme "${t.name}"?${t.shared ? ' Anyone using it goes back to the default.' : ''}`)) return;
    try {
      await api('DELETE', `/themes/${t.id}`);
      state.user = await api('GET', '/me');
      useUserTheme();
      await reload();
      draw();
    } catch (e) { toast(e.message); }
  };

  const card = (key, name, vars, sub, actions) => {
    const on = current() === key;
    return h('div', { class: 'theme-card' + (on ? ' on' : '') },
      h('button', { class: 'theme-pick', type: 'button', title: `Use ${name}`, onclick: () => choose(key) },
        themeSwatch(vars),
        h('span', { class: 'theme-name' }, name, on ? ' ✓' : ''),
        h('span', { class: 'muted xs' }, sub)),
      h('div', { class: 'theme-actions' }, actions.filter(Boolean)));
  };
  const copyBtn = (name, vars) => h('button', { class: 'link xs', onclick: () => edit(null, `${name} (copy)`, vars) }, 'Copy');

  const draw = () => {
    root.replaceChildren(
      h('h4', {}, 'Presets'),
      h('div', { class: 'theme-grid' }, Object.entries(THEME_PRESETS).map(([k, p]) =>
        card(`preset:${k}`, p.name, p.vars, 'Preset', [copyBtn(p.name, p.vars)]))),
      h('div', { class: 'row between' },
        h('h4', {}, 'Your themes'),
        h('button', { class: 'btn sm primary', onclick: () => edit(null, 'My theme', userThemeVars(state.user)) }, '+ New theme')),
      data.mine.length
        ? h('div', { class: 'theme-grid' }, data.mine.map(t => card(`custom:${t.id}`, t.name, t.vars, t.shared ? 'Yours · shared' : 'Yours', [
          h('button', { class: 'link xs', onclick: () => edit(t) }, 'Edit'),
          h('button', { class: 'link xs danger', onclick: () => del(t) }, 'Delete'),
        ])))
        : h('p', { class: 'muted xs' }, 'Make your own with + New theme, or Copy any theme here to tweak it.'),
      h('h4', {}, 'Shared themes'),
      data.shared.length
        ? h('div', { class: 'theme-grid' }, data.shared.map(t => card(`custom:${t.id}`, t.name, t.vars, `by ${t.owner}`, [
          copyBtn(t.name, t.vars),
          state.user.isAdmin ? h('button', { class: 'link xs danger', onclick: () => del(t) }, 'Delete') : null,
        ])))
        : h('p', { class: 'muted xs' }, 'Nobody has shared a theme yet. Tick "Share with everyone" on one of yours.'));
  };

  // edit shows the editor for one of your themes (theme), or a new one
  // starting from the given name and colours.
  const edit = (theme, startName, startVars) => {
    const vars = { ...THEME_PRESETS.penguin.vars, ...(theme ? theme.vars : startVars) };
    const name = h('input', { value: theme ? theme.name : startName, maxlength: 40, 'aria-label': 'Theme name' });
    const shared = h('input', { type: 'checkbox', checked: theme ? theme.shared : false });
    const field = ([key, , label]) => {
      const text = h('input', { class: 'hex', value: vars[key], maxlength: 7, 'aria-label': `${label} (hex)` });
      const pick = h('input', { type: 'color', value: vars[key], 'aria-label': label });
      pick.addEventListener('input', () => {
        vars[key] = pick.value;
        text.value = pick.value;
        applyTheme(vars);
      });
      text.addEventListener('input', () => {
        const v = text.value.trim().toLowerCase();
        if (/^#[0-9a-f]{6}$/.test(v)) {
          vars[key] = v;
          pick.value = v;
          applyTheme(vars);
        }
      });
      return h('label', { class: 'theme-field' }, pick, h('span', { class: 'grow' }, label), text);
    };
    const save = async () => {
      try {
        const body = { name: name.value, vars, shared: shared.checked };
        const saved = theme ? await api('PATCH', `/themes/${theme.id}`, body) : await api('POST', '/themes', body);
        state.user = await api('PUT', '/me/theme', { theme: `custom:${saved.id}` });
        useUserTheme();
        await reload();
        draw();
        toast(`Saved "${saved.name}"`, 'ok');
      } catch (e) { toast(e.message); }
    };
    root.replaceChildren(
      h('label', { class: 'field' }, h('span', {}, 'Name'), name),
      h('label', { class: 'check' }, shared, 'Share with everyone (they can use it or copy it)'),
      h('p', { class: 'muted xs' }, 'Changes preview live on the whole page. Cancel puts your current theme back.'),
      h('div', { class: 'theme-fields' }, THEME_FIELDS.map(field)),
      h('details', { class: 'manage' },
        h('summary', {}, 'Column & tag colours'),
        h('div', { class: 'theme-fields' }, THEME_COLUMN_FIELDS.map(field))),
      h('div', { class: 'row between' },
        h('button', { class: 'btn ghost', onclick: () => { useUserTheme(); draw(); } }, 'Cancel'),
        h('button', { class: 'btn primary', onclick: save }, theme ? 'Save changes' : 'Save theme')));
    applyTheme(vars);
  };

  openModal('Themes', root, true);
  // However the window is closed, an unsaved preview shouldn't stick.
  state.onModalClose = useUserTheme;
  root.replaceChildren(h('p', { class: 'muted' }, 'Loading…'));
  await reload();
  draw();
}

function openPasswordModal() {
  const current = h('input', { type: 'password', autocomplete: 'current-password', required: true, placeholder: 'Current password' });
  const next = h('input', { type: 'password', autocomplete: 'new-password', required: true, minlength: 8, maxlength: 72, placeholder: 'New password (8+ characters)' });
  const confirmPw = h('input', { type: 'password', autocomplete: 'new-password', required: true, placeholder: 'Confirm new password' });
  const form = h('form', {
    class: 'stack',
    onsubmit: async e => {
      e.preventDefault();
      if (next.value !== confirmPw.value) return toast('New passwords do not match');
      try {
        await api('POST', '/password', { current: current.value, new: next.value });
        closeModal();
        toast('Password changed. Other sessions were logged out.', 'ok');
      } catch (ex) { toast(ex.message); }
    },
  }, current, next, confirmPw, h('button', { class: 'btn primary', type: 'submit' }, 'Save'));
  openModal('Change Password', form);
  current.focus();
}

// ---------- layout ----------

function clockText() {
  return new Date().toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
}
setInterval(() => {
  const c = document.getElementById('clock');
  if (c) c.textContent = clockText();
}, 15000);

// setAdminMode switches an admin between view and edit mode on boards they
// don't own or edit, then redraws the current page with the new permissions.
async function setAdminMode(on) {
  try {
    state.user = await api('PUT', '/me/admin-mode', { on });
    toast(on ? 'Admin edit mode on: you can change any board' : 'Admin edit mode off', 'ok');
    state.board = null;
    boardCache.clear(); // remembered boards have the old permissions
    route();
  } catch (e) { toast(e.message); }
}

// menubar is the top bar. On phones its items fold into a ☰ Menu dropdown.
function menubar(adminLink, modeSwitch) {
  const items = h('div', { class: 'menu-items' },
    h('a', { href: '#/', class: 'menu-item' }, 'Boards'),
    h('a', { href: '#/music', class: 'menu-item' }, 'Music'),
    adminLink,
    modeSwitch,
    h('button', { class: 'menu-item', onclick: openProfileModal }, 'Profile'),
    h('button', { class: 'menu-item', onclick: openThemesModal }, 'Themes'),
    h('button', { class: 'menu-item', onclick: openPasswordModal }, 'Password'),
    h('button', { class: 'menu-item', onclick: logout }, 'Log Out'),
    h('div', { class: 'spacer' }),
    h('span', { class: 'menu-item static whoami' }, avatar(state.user.id, state.user.username, 'tiny'), state.user.username),
    h('span', { class: 'menu-item static', id: 'clock' }, clockText()));
  const toggle = h('button', {
    class: 'menu-item menu-toggle', 'aria-expanded': 'false', 'aria-label': 'Menu',
    onclick: () => toggle.setAttribute('aria-expanded', String(bar.classList.toggle('open'))),
  }, '☰ Menu');
  const bar = h('header', { class: 'menubar' },
    h('a', { href: '#/', class: 'brand', 'aria-label': 'pingugamer home' }, h('img', { src: '/logo.png', alt: '' }), h('span', { class: 'brand-text' }, 'pingugamer')),
    toggle,
    items);
  // Picking anything in the dropdown closes it.
  items.addEventListener('click', () => {
    bar.classList.remove('open');
    toggle.setAttribute('aria-expanded', 'false');
  });
  return bar;
}

// shellKey describes what the frame around the page (menu bar, chat,
// footer) shows. While it stays the same, moving between pages swaps only the
// page itself, so the chat, its pictures and the ticker don't start over.
function shellKey() {
  const b = feedBoard();
  const me = state.avatars[state.user.id];
  return [state.user.id, state.user.username, state.user.adminMode, me ? me.v : '', channelKey(), b ? `${b.id}:${b.canEdit}` : ''].join('|');
}

function shell(...content) {
  const main = h('main', { class: 'main' }, ...content);
  const key = shellKey();
  const old = $app.querySelector(':scope > .layout > main');
  if (old && $app.dataset.shell === key) {
    old.replaceWith(main);
    refreshAdminBadge();
    if (!chan().loadedAt || Date.now() - chan().loadedAt > 5000) loadFeed();
    return;
  }
  $app.dataset.shell = key;
  let adminLink = null;
  let modeSwitch = null;
  if (state.user.isAdmin) {
    const on = state.user.adminMode;
    modeSwitch = h('button', {
      class: 'menu-item mode-switch' + (on ? ' on' : ''),
      title: on ? 'You can edit every board. Click to go back to view mode.' : 'Boards you aren\'t an editor of are view only. Click to edit them as an admin.',
      onclick: () => setAdminMode(!on),
    }, h('span', { class: 'mode-light', 'aria-hidden': 'true' }), on ? 'Admin edit mode' : 'Admin view mode');
  }
  if (state.user.isAdmin) {
    adminLink = h('a', { href: '#/admin', class: 'menu-item' }, 'Admin', h('span', { class: 'nav-badge', hidden: true }));
  }
  // replaceChildren would print a null as "null", hence the filter at the end.
  $app.replaceChildren(...[
    menubar(adminLink, modeSwitch),
    h('div', { class: `layout feed-${state.feedSide}${state.feedHidden ? ' feed-hidden' : ''}` },
      feedPanel(),
      main),
    state.feedHidden ? h('button', { class: `btn feed-reopen feed-reopen-${state.feedSide}`, onclick: () => setFeedHidden(false) }, '✎ Chat') : null,
    h('footer', { class: 'footer' },
      h('div', { class: 'badges88' },
        h('span', { class: 'b88 b88-go' }, h('b', {}, 'GO'), h('span', {}, 'powered')),
        h('span', { class: 'b88 b88-sql' }, h('b', {}, 'SQLite'), h('span', {}, 'inside')),
        h('span', { class: 'b88 b88-view' }, h('b', {}, '800×600'), h('span', {}, 'best viewed')),
        h('span', { class: 'b88 b88-peng' }, h('img', { src: '/logo.png', alt: '' }), h('span', {}, 'penguin approved'))),
      h('div', { class: 'marquee', id: 'ticker' }, h('span', {})),
      h('button', { class: 'ticker-add', onclick: openTickerModal }, '✎ add to the ticker')),
  ].filter(Boolean));
  refreshAdminBadge();
  drawTicker();
  if (!state.ticker || Date.now() - state.tickerAt > 120000) loadTicker();
  drawFeed();
  if (!chan().loadedAt || Date.now() - chan().loadedAt > 5000) loadFeed();
}

// refreshAdminBadge shows admins how many sign-ups are waiting.
function refreshAdminBadge() {
  if (!state.user.isAdmin) return;
  api('GET', '/admin/pending').then(p => {
    const badge = document.querySelector('.menubar .nav-badge');
    if (!badge) return;
    badge.textContent = String(p.length);
    badge.hidden = p.length === 0;
  }).catch(() => {});
}

// ---------- chat feed ----------

const POST_MAX = 280;
const isMobile = () => window.matchMedia('(max-width: 760px)').matches;

function setFeedSide(side) {
  state.feedSide = side;
  store('feedSide', side);
  relayout();
  drawMusic(); // the music player sits on the other side
}

function setFeedHidden(hidden) {
  state.feedHidden = hidden;
  store('feedHidden', hidden ? '1' : '0');
  relayout();
  if (!hidden) loadFeed();
}

// relayout moves or hides the feed without redrawing the page under it.
function relayout() {
  const layout = document.querySelector('.layout');
  if (!layout) return;
  layout.className = `layout feed-${state.feedSide}${state.feedHidden ? ' feed-hidden' : ''}`;
  document.querySelector('.feed-reopen')?.remove();
  if (state.feedHidden) {
    layout.after(h('button', { class: `btn feed-reopen feed-reopen-${state.feedSide}`, onclick: () => setFeedHidden(false) }, '✎ Chat'));
  }
}

// Chat has channels: the site-wide one, and one per board. On a board's page
// the chat window has tabs for both; elsewhere it's site-wide only.

// feedBoard is the board whose page is showing, if any.
function feedBoard() {
  const m = location.hash.match(/^#\/board\/(\d+)/);
  return m && state.board && state.board.id === Number(m[1]) ? state.board : null;
}

function channelKey() {
  const b = feedBoard();
  return b && state.feedTab === 'board' ? `board:${b.id}` : 'site';
}

// chan returns the channel being shown, creating its state on first use.
function chan() {
  const key = channelKey();
  if (!state.channels[key]) {
    state.channels[key] = {
      key, boardId: key === 'site' ? null : feedBoard().id,
      posts: [], more: false, loadedAt: 0, etag: null, draft: '',
      game: null, picking: false, music: null, playlist: null, musicPicking: false,
    };
  }
  return state.channels[key];
}

const feedPath = c => (c.boardId ? `/boards/${c.boardId}/feed` : '/feed');

function setFeedTab(tab) {
  state.feedTab = tab;
  store('feedTab', tab);
  const old = document.getElementById('feed');
  if (old) old.replaceWith(feedPanel());
  if (state.user) $app.dataset.shell = shellKey();
  drawFeed();
  loadFeed();
}

// feedPanel builds the chat window. The compose box is built once per page so
// typing isn't interrupted; drawFeed refreshes just the list of posts.
function feedPanel() {
  const f = chan();
  const board = feedBoard();
  const counter = h('span', { class: 'muted xs' });
  const updateCounter = () => { counter.textContent = String(POST_MAX - f.draft.length); };
  const ta = h('textarea', {
    rows: 3, maxlength: POST_MAX, placeholder: "What's happening?", value: f.draft, 'aria-label': 'New post',
    oninput: e => { f.draft = e.target.value; updateCounter(); },
    onkeydown: e => { if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') form.requestSubmit(); },
  });
  const linkArea = h('div', { class: 'feed-link' });
  const linkBtn = h('button', {
    class: 'link xs', type: 'button',
    onclick: () => { f.picking = !f.picking && !f.game; drawLinkArea(linkArea, linkBtn); },
  });
  const musicArea = h('div', { class: 'feed-link' });
  const musicBtn = h('button', {
    class: 'link xs', type: 'button',
    onclick: () => { f.musicPicking = !f.musicPicking && !f.music && !f.playlist; drawMusicArea(musicArea, musicBtn); },
  });
  const postBtn = h('button', { class: 'btn primary sm', type: 'submit' }, 'Post');
  const form = h('form', {
    class: 'feed-compose',
    onsubmit: async e => {
      e.preventDefault();
      const body = f.draft.trim();
      // A shared game or song can go out without a caption.
      if (!body && !f.game && !f.music && !f.playlist) return;
      postBtn.disabled = true;
      try {
        const g = f.game;
        const p = await api('POST', feedPath(f), {
          body,
          gameId: g && !g.external ? g.id : undefined,
          steamAppId: g && g.external ? g.steamAppId : undefined,
          igdbId: g && g.external ? g.igdbId : undefined,
          musicId: f.music ? f.music.id : undefined,
          playlistId: f.playlist ? f.playlist.id : undefined,
        });
        f.posts.unshift(p);
        f.draft = '';
        f.game = null;
        f.picking = false;
        f.music = null;
        f.playlist = null;
        f.musicPicking = false;
        ta.value = '';
        updateCounter();
        drawLinkArea(linkArea, linkBtn);
        drawMusicArea(musicArea, musicBtn);
        drawFeed();
      } catch (ex) { toast(ex.message); } finally { postBtn.disabled = false; }
    },
  },
  ta,
  linkArea,
  musicArea,
  h('div', { class: 'row between' }, h('div', { class: 'row' }, linkBtn, musicBtn), h('div', { class: 'row' }, counter, postBtn)));
  updateCounter();
  drawLinkArea(linkArea, linkBtn);
  drawMusicArea(musicArea, musicBtn);

  // Board chats can be read by anyone but only its editors can post.
  const compose = !board || state.feedTab !== 'board' || board.canEdit
    ? form
    : h('p', { class: 'feed-readonly' }, "Only this board's editors can post here. Anyone can read along.");
  const tabs = board ? h('div', { class: 'feed-tabs', role: 'tablist' },
    [['site', 'Everyone'], ['board', 'This board']].map(([tab, label]) => h('button', {
      type: 'button', role: 'tab', class: 'feed-tab' + (state.feedTab === tab ? ' on' : ''),
      'aria-selected': state.feedTab === tab ? 'true' : 'false',
      title: tab === 'board' ? `Chat for ${board.name}` : 'Chat for everyone on the site',
      onclick: () => { if (state.feedTab !== tab) setFeedTab(tab); },
    }, label))) : null;

  const sideBtn = h('button', {
    class: 'feed-side', type: 'button', title: 'Move chat to the other side', 'aria-label': 'Move chat to the other side',
    onclick: () => setFeedSide(state.feedSide === 'left' ? 'right' : 'left'),
  }, '⇄');
  const panel = win('Chat', {
    cls: 'feed-win',
    extra: sideBtn,
    onClose: () => {
      if (isMobile()) toggleMobile(false);
      else setFeedHidden(true);
    },
  }, tabs, compose, h('div', { class: 'feed-list', id: 'feed-list' }));

  const aside = h('aside', { class: 'feed' + (state.feedMobileOpen ? ' open' : ''), id: 'feed' });
  const toggleMobile = open => {
    state.feedMobileOpen = open;
    aside.classList.toggle('open', open);
    if (open) loadFeed();
  };
  aside.append(h('button', { class: 'btn feed-mobile-toggle', type: 'button', onclick: () => toggleMobile(!state.feedMobileOpen) }, '✎ Chat'), panel);
  return aside;
}

// closeOnOutside closes a composer picker (in area, opened by toggle) when
// someone clicks anywhere else. Redrawing the area clears it first.
function closeOnOutside(area, toggle, close) {
  area.closePicker = e => {
    if (!area.isConnected) {
      document.removeEventListener('pointerdown', area.closePicker);
      return;
    }
    if (!area.contains(e.target) && !toggle.contains(e.target)) close();
  };
  document.addEventListener('pointerdown', area.closePicker);
}

function clearOutsideClose(area) {
  if (area.closePicker) {
    document.removeEventListener('pointerdown', area.closePicker);
    area.closePicker = null;
  }
}

// drawLinkArea shows the game linked to the post being written, or the picker.
// The picker closes on a click anywhere outside it, or on Escape.
function drawLinkArea(area, toggle) {
  const f = chan();
  clearOutsideClose(area);
  const redraw = () => drawLinkArea(area, toggle);
  toggle.textContent = f.picking ? 'Cancel' : '+ Link a game';
  toggle.hidden = !!f.game;
  if (f.game) {
    area.replaceChildren(gameChip(f.game, () => { f.game = null; redraw(); }));
    return;
  }
  if (!f.picking) {
    area.replaceChildren();
    return;
  }
  const close = () => { f.picking = false; redraw(); };
  closeOnOutside(area, toggle, close);

  const q = h('input', {
    placeholder: 'Search boards, Steam & IGDB, or paste a link…', 'aria-label': 'Search games to link',
    onkeydown: e => { if (e.key === 'Escape') { e.stopPropagation(); close(); } },
  });
  const results = h('div', { class: 'feed-results' });
  let timer, seq = 0;
  const pick = g => () => { f.game = g; f.picking = false; redraw(); };
  const row = (g, sub) => h('button', { type: 'button', class: 'feed-result', onclick: pick(g) },
    chipThumb(g), h('span', { class: 'chip-text' }, h('span', { class: 'chip-title' }, g.title), h('span', { class: 'muted xs' }, sub)));
  // Board games come first; Steam and IGDB results follow once there's something to search for.
  const search = async () => {
    const my = ++seq;
    const text = q.value.trim();
    let onBoards, outside = null;
    try {
      [onBoards, outside] = await Promise.all([
        api('GET', '/feed/games?q=' + encodeURIComponent(text)),
        text.length >= 2 ? api('GET', '/search?q=' + encodeURIComponent(text)) : null,
      ]);
    } catch (e) { return toast(e.message); }
    if (my !== seq) return;
    const groups = [];
    if (onBoards.length) groups.push(h('h4', {}, 'On the boards'), onBoards.map(g => row(g, g.boardName)));
    if (outside) {
      const ext = (r, source) => ({ external: true, title: r.title, image: r.image, steamAppId: r.steamAppId, igdbId: r.igdbId, source });
      if (outside.steam.length) groups.push(h('h4', {}, 'Steam'), outside.steam.slice(0, 6).map(r => row(ext(r, 'Steam'), [r.price, 'Steam'].filter(Boolean).join(' · '))));
      if (outside.igdb.length) groups.push(h('h4', {}, 'IGDB'), outside.igdb.slice(0, 6).map(r => row(ext(r, 'IGDB'), [r.year, 'IGDB'].filter(Boolean).join(' · '))));
    }
    if (!groups.length) groups.push(h('p', { class: 'muted xs' }, text.length >= 2 ? 'No games found.' : 'Type to search Steam and IGDB too.'));
    results.replaceChildren(...groups.flat());
  };
  q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(search, 250); });
  area.replaceChildren(q, results);
  q.focus();
  search(); // empty search lists recently updated games
}

function chipThumb(g) {
  const img = safeUrl(g.image);
  return img ? h('img', { src: img, alt: '', loading: 'lazy' }) : h('span', { class: 'chip-ph' }, g.title.slice(0, 1));
}

// gameChip shows a linked game: one on a board, or (external) one from
// Steam/IGDB. With onRemove it's the composer's chip; otherwise clicking it
// opens the game.
function gameChip(g, onRemove) {
  const sub = g.external ? (g.steamAppId ? 'Steam' : 'IGDB') : g.boardName;
  const text = h('span', { class: 'chip-text' }, h('span', { class: 'chip-title' }, g.title), h('span', { class: 'muted xs' }, sub));
  if (onRemove) {
    return h('div', { class: 'game-chip' }, chipThumb(g), text,
      h('button', { class: 'link xs danger', type: 'button', onclick: onRemove, 'aria-label': 'Remove linked game' }, '×'));
  }
  const open = g.external ? () => openExternalGame(g) : () => openLinkedGame(g);
  return h('button', { class: 'game-chip', type: 'button', title: `Open ${g.title}`, onclick: open }, chipThumb(g), text);
}

// openExternalGame shows a Steam/IGDB game from the chat with its store links
// and a way to put it on one of your boards.
async function openExternalGame(g) {
  let boards;
  try { boards = (await api('GET', '/boards')).filter(b => b.canEdit); } catch (e) { return toast(e.message); }
  const links = [];
  if (g.steamAppId) {
    links.push(['Steam', `https://store.steampowered.com/app/${g.steamAppId}/`],
      ['SteamDB', `https://steamdb.info/app/${g.steamAppId}/`],
      ['ProtonDB', `https://www.protondb.com/app/${g.steamAppId}`]);
  }
  if (safeUrl(g.igdbUrl)) links.push(['IGDB', g.igdbUrl]);

  let addRow;
  if (boards.length) {
    const pick = h('select', { 'aria-label': 'Board' }, boards.map(b => h('option', { value: b.id }, b.name)));
    const btn = h('button', {
      class: 'btn primary sm',
      onclick: async () => {
        btn.disabled = true;
        try {
          const added = await api('POST', `/boards/${pick.value}/games`, { title: g.title, steamAppId: g.steamAppId, igdbId: g.igdbId });
          closeModal();
          toast(`Added ${added.title}`, 'ok');
          if (state.board && state.board.id === added.boardId) {
            syncGame(added);
          }
        } catch (e) {
          toast(e.message);
          btn.disabled = false;
        }
      },
    }, 'Add');
    addRow = h('div', { class: 'field' }, h('span', {}, 'Add to a board'), h('div', { class: 'row' }, pick, btn));
  } else {
    addRow = h('p', { class: 'muted xs' }, "You aren't an editor of any boards yet, so you can't add it anywhere.");
  }

  const img = safeUrl(g.image);
  openModal('Game', h('div', { class: 'game-detail' },
    h('div', { class: 'hero' + (img ? '' : ' no-img') },
      img ? h('img', { src: img, alt: '' }) : null,
      h('div', { class: 'hero-text' }, h('h2', {}, g.title))),
    links.length ? h('div', { class: 'link-row' }, links.map(([label, u]) =>
      h('a', { class: 'btn sm', href: u, target: '_blank', rel: 'noopener noreferrer' }, label + ' ↗'))) : null,
    addRow));
}

function openLinkedGame(g) {
  const board = feedBoard();
  if (board && board.id === g.boardId) {
    openGame(g.id);
    return;
  }
  state.pendingGame = g.id;
  location.hash = `#/board/${g.boardId}`;
}

function postItem(p, c) {
  const mine = p.userId === state.user.id;
  // Admins can remove any post; a board's owner can tidy up its chat.
  const board = feedBoard();
  const canRemove = mine || state.user.isAdmin || (c.boardId && board && board.canManage);
  const del = async () => {
    if (!confirm(mine ? 'Delete your post?' : `Remove this post by ${p.username}?`)) return;
    try {
      await api('DELETE', `/feed/${p.id}`);
      c.posts = c.posts.filter(x => x.id !== p.id);
      drawFeed();
    } catch (e) { toast(e.message); }
  };
  return h('article', { class: 'post' },
    h('div', { class: 'post-head' },
      avatar(p.userId, p.username, 'none'),
      h('strong', {}, p.username),
      h('span', { class: 'muted xs' }, ago(p.createdAt)),
      h('span', { class: 'spacer' }),
      canRemove ? h('button', { class: 'link xs danger', onclick: del }, mine ? 'Delete' : 'Remove') : null),
    p.body ? h('div', { class: 'post-body' }, linkify(p.body)) : null,
    p.music ? musicChip(p.music) : null,
    p.playlist ? playlistChip(p.playlist) : null,
    p.game ? gameChip(p.game) : null,
    p.link ? gameChip({ ...p.link, external: true }) : null,
    p.hadAttachment ? h('p', { class: 'muted xs post-gone' }, 'Shared something that has since been deleted.') : null);
}

function drawFeed() {
  const list = document.getElementById('feed-list');
  if (!list) return;
  const c = chan();
  const items = c.posts.map(p => postItem(p, c));
  if (!c.loadedAt) items.push(h('p', { class: 'muted xs' }, 'Loading…'));
  else if (!c.posts.length) items.push(h('p', { class: 'muted xs' }, c.boardId ? 'No messages on this board yet.' : 'Nothing here yet. Say something!'));
  if (c.more) items.push(h('button', { class: 'btn sm block', onclick: loadOlderPosts }, 'Load older posts'));
  list.replaceChildren(...items);
}

// loadFeed refreshes the shown channel's newest page, keeping any older pages
// already loaded. A reply for a channel that's no longer showing is still kept.
async function loadFeed() {
  const c = chan();
  try {
    const r = await apiPoll(feedPath(c), c.loadedAt ? c.etag : null);
    if (!r) { // nothing new
      c.loadedAt = Date.now();
      return;
    }
    const d = r.data;
    c.etag = r.etag;
    const oldest = d.posts.length ? d.posts[d.posts.length - 1].id : 0;
    const older = d.posts.length ? c.posts.filter(p => p.id < oldest) : [];
    c.posts = d.posts.concat(older);
    if (!older.length) c.more = d.more;
    c.loadedAt = Date.now();
    if (chan() === c) drawFeed();
  } catch { /* try again on the next refresh */ }
}

async function loadOlderPosts() {
  const c = chan();
  const last = c.posts[c.posts.length - 1];
  if (!last) return;
  try {
    const d = await api('GET', `${feedPath(c)}?before=${last.id}`);
    c.posts = c.posts.concat(d.posts);
    c.more = d.more;
    if (chan() === c) drawFeed();
  } catch (e) { toast(e.message); }
}

// Refresh the feed every 20s while it's on screen.
setInterval(() => {
  if (!state.user || document.hidden || !document.getElementById('feed')) return;
  if (isMobile() ? state.feedMobileOpen : !state.feedHidden) loadFeed();
}, 20000);

// ---------- music ----------

// Music is Spotify and YouTube (Music) links that the server has checked and
// looked up. It's shared in chat, attached to games, and played in a
// floating player that lives outside the page content, so it keeps playing
// while people move around the site. Nothing from Spotify or YouTube loads
// until someone presses play.

const MUSIC_KINDS = { track: 'Song', album: 'Album', playlist: 'Playlist', episode: 'Episode', show: 'Podcast', artist: 'Artist', video: 'Song' };
const MUSIC_ALSO = [['spotify', 'Spotify'], ['youtubeMusic', 'YouTube Music'], ['appleMusic', 'Apple Music']];
const SPOTIFY_LOGIN = 'https://accounts.spotify.com/login?continue=https%3A%2F%2Fopen.spotify.com%2F';

const providerName = m => (m.provider === 'spotify' ? 'Spotify' : 'YouTube');

// alsoLinks searches for a song on the other services.
function alsoLinks(m) {
  // YouTube titles carry extras like "(Official Video)" and channels end in
  // " - Topic" or "VEVO"; drop those, and the artist if the title has it.
  const title = m.title.replace(/\s*[([][^)\]]*\b(official|video|audio|lyrics?|remaster(ed)?|visuali[sz]er|hd|4k|mv)\b[^)\]]*[)\]]/gi, '').trim() || m.title;
  const artist = (m.artist || '').replace(/\s*-\s*Topic$|VEVO$/i, '').trim();
  const words = artist && !title.toLowerCase().includes(artist.toLowerCase()) ? [title, artist] : [title];
  const q = encodeURIComponent(words.join(' ').slice(0, 120));
  const search = {
    spotify: `https://open.spotify.com/search/${q}`,
    youtubeMusic: `https://music.youtube.com/search?q=${q}`,
    appleMusic: `https://music.apple.com/us/search?term=${q}`,
  };
  const mine = m.provider === 'spotify' ? 'spotify' : 'youtubeMusic';
  return MUSIC_ALSO.filter(([key]) => key !== mine)
    .map(([key, label]) => h('a', { href: search[key], target: '_blank', rel: 'noopener noreferrer', title: `Search ${label}` }, label))
    .flatMap((a, i) => (i ? [', ', a] : [a]));
}

// resolveMusic asks the server to check a pasted link and look it up.
const resolveMusic = link => api('POST', '/music/resolve', { url: link });

// musicChip shows a song or Spotify/YouTube playlist with play / queue /
// add-to-playlist buttons, a link to it, and where else it's available.
// onRemove adds a remove button; opts.onPlay replaces what ▶ does and
// opts.extra adds more buttons (a playlist page's ↑ ↓).
function musicChip(m, onRemove, opts = {}) {
  const thumb = safeUrl(m.thumb);
  const chip = h('div', { class: 'music-chip' },
    thumb ? h('img', { src: thumb, alt: '', loading: 'lazy' }) : h('span', { class: 'chip-ph' }, '♪'),
    h('span', { class: 'chip-text' },
      h('span', { class: 'chip-title', title: m.title }, m.title),
      h('span', { class: 'muted xs' }, [m.artist, MUSIC_KINDS[m.kind], providerName(m)].filter(Boolean).join(' · ')),
      h('span', { class: 'music-links xs' },
        h('a', { href: safeUrl(m.url), target: '_blank', rel: 'noopener noreferrer' }, `Open in ${providerName(m)} ↗`),
        ' · Find on ', alsoLinks(m))),
    h('span', { class: 'music-actions' },
      opts.extra,
      h('button', { class: 'btn sm primary', type: 'button', title: 'Play now', 'aria-label': `Play ${m.title}`, onclick: opts.onPlay || (() => playNow(m)) }, '▶'),
      h('button', { class: 'btn sm', type: 'button', title: 'Add to queue', 'aria-label': `Queue ${m.title}`, onclick: () => queueAdd(m) }, '+'),
      h('button', { class: 'btn sm', type: 'button', title: 'Add to a playlist', 'aria-label': `Add ${m.title} to a playlist`, onclick: e => toggleAddToPlaylist(chip, m, e.currentTarget) }, '♫+'),
      onRemove ? h('button', { class: 'link xs danger', type: 'button', title: 'Remove', 'aria-label': 'Remove', onclick: onRemove }, '×') : null));
  return chip;
}

// ---- our own playlists ----

// loadPlaylists fetches everyone's playlists (summaries, no songs).
async function loadPlaylists() {
  state.playlists = await api('GET', '/playlists');
  return state.playlists;
}

const myPlaylists = list => list.filter(p => p.userId === state.user.id);
const songCount = n => `${n} song${n === 1 ? '' : 's'}`;

async function playlistSongs(pl) {
  const full = await api('GET', `/playlists/${pl.id}`);
  if (!full.items.length) throw new Error(`"${full.name}" has no songs yet`);
  return full.items;
}

async function playPlaylist(pl) {
  try { playAll(await playlistSongs(pl)); } catch (e) { toast(e.message); }
}

async function queuePlaylist(pl) {
  try {
    const items = await playlistSongs(pl);
    const wasEmpty = !player.queue.length;
    player.queue.push(...items);
    if (wasEmpty) {
      playIndex(0);
      return;
    }
    saveMusic();
    drawMusic();
    toast(`Queued ${songCount(items.length)} from ${pl.name}`, 'ok');
  } catch (e) { toast(e.message); }
}

// playlistChip shows one of our playlists: its name, owner and size, a link
// to its page, and play / queue buttons. onRemove adds a remove button.
function playlistChip(pl, onRemove) {
  const thumb = safeUrl(pl.thumb);
  const href = `#/music/playlist/${pl.id}`;
  return h('div', { class: 'music-chip playlist-chip' },
    h('a', { class: 'playlist-art', href, 'aria-hidden': 'true', tabindex: '-1' },
      thumb ? h('img', { src: thumb, alt: '', loading: 'lazy' }) : h('span', { class: 'chip-ph' }, '♫')),
    h('span', { class: 'chip-text' },
      h('a', { class: 'chip-title', href, title: pl.name }, pl.name),
      h('span', { class: 'muted xs' }, `Playlist · ${songCount(pl.count)} · by ${pl.username}`)),
    h('span', { class: 'music-actions' },
      h('button', { class: 'btn sm primary', type: 'button', title: 'Play the playlist', 'aria-label': `Play ${pl.name}`, disabled: !pl.count, onclick: () => playPlaylist(pl) }, '▶'),
      h('button', { class: 'btn sm', type: 'button', title: 'Add it all to the queue', 'aria-label': `Queue ${pl.name}`, disabled: !pl.count, onclick: () => queuePlaylist(pl) }, '+'),
      onRemove ? h('button', { class: 'link xs danger', type: 'button', title: 'Remove', 'aria-label': 'Remove', onclick: onRemove }, '×') : null));
}

// toggleAddToPlaylist opens (or closes) a row under a song for adding it to
// one of your playlists, or to a new one.
async function toggleAddToPlaylist(chip, m, toggle) {
  const open = chip.querySelector('.chip-add');
  if (open) {
    clearOutsideClose(open);
    open.remove();
    return;
  }
  let mine;
  try { mine = myPlaylists(await loadPlaylists()); } catch (e) { return toast(e.message); }
  const NEW = 'new';
  const pick = h('select', { 'aria-label': 'Playlist' },
    mine.map(p => h('option', { value: p.id }, `${p.name} (${p.count})`)),
    h('option', { value: NEW }, '+ New playlist…'));
  const name = h('input', { placeholder: 'New playlist name', maxlength: 60, 'aria-label': 'New playlist name' });
  const add = h('button', { class: 'btn sm primary', type: 'button' }, 'Add');
  const showName = () => { name.hidden = pick.value !== NEW; if (!name.hidden) name.focus(); };
  const close = () => { clearOutsideClose(row); row.remove(); };
  const submit = async () => {
    add.disabled = true;
    try {
      let pl;
      if (pick.value === NEW) {
        if (!name.value.trim()) { name.focus(); return; }
        pl = await api('POST', '/playlists', { name: name.value, musicId: m.id });
        toast(`Made "${pl.name}" with ${m.title}`, 'ok');
      } else {
        pl = await api('POST', `/playlists/${pick.value}/items`, { musicId: m.id });
        toast(`Added to ${pl.name}`, 'ok');
      }
      close();
      if (state.onPlaylistChange) state.onPlaylistChange(pl);
    } catch (e) { toast(e.message); } finally { add.disabled = false; }
  };
  pick.addEventListener('change', showName);
  add.addEventListener('click', submit);
  name.addEventListener('keydown', e => {
    if (e.key === 'Enter') { e.preventDefault(); submit(); }
  });
  const row = h('div', { class: 'chip-add row', onkeydown: e => { if (e.key === 'Escape') { e.stopPropagation(); close(); } } },
    h('span', { class: 'xs' }, 'Add to'), pick, name, add);
  chip.append(row);
  closeOnOutside(row, toggle, close);
  showName();
  if (mine.length) pick.focus();
}

// musicLinkBox is a "paste a link" box; onMusic gets the looked-up music.
// With onPlaylist, it also offers our own playlists to pick from.
// It isn't a <form>, because it also sits inside the chat's form.
function musicLinkBox(onMusic, onCancel, onPlaylist) {
  const box = pasteMusicRow(onMusic, onCancel);
  if (!onPlaylist) return box;
  const pick = h('select', { 'aria-label': 'Link a playlist', disabled: true }, h('option', { value: '' }, 'Loading playlists…'));
  loadPlaylists().then(list => {
    const mine = myPlaylists(list);
    const others = list.filter(p => p.userId !== state.user.id);
    const opt = p => h('option', { value: p.id }, `${p.name} (${songCount(p.count)}${p.userId === state.user.id ? '' : `, by ${p.username}`})`);
    pick.replaceChildren(
      h('option', { value: '' }, list.length ? '…or pick a playlist' : 'No playlists yet (make one on the Music page)'),
      mine.length ? h('optgroup', { label: 'Your playlists' }, mine.map(opt)) : null,
      others.length ? h('optgroup', { label: "Everyone else's" }, others.map(opt)) : null);
    pick.disabled = !list.length;
  }).catch(e => toast(e.message));
  pick.addEventListener('change', async () => {
    const pl = (state.playlists || []).find(p => String(p.id) === pick.value);
    if (!pl) return;
    pick.disabled = true;
    try { await onPlaylist(pl); } catch (e) { toast(e.message); pick.disabled = false; }
  });
  return h('div', { class: 'stack tight' }, box, pick);
}

function pasteMusicRow(onMusic, onCancel) {
  const add = h('button', { class: 'btn sm primary', type: 'button' }, 'Add');
  const input = h('input', {
    placeholder: 'Paste a Spotify or YouTube Music link…', 'aria-label': 'Music link',
    onkeydown: e => {
      if (e.key === 'Enter') {
        e.preventDefault(); // don't send the chat post around it
        submit();
      } else if (e.key === 'Escape' && onCancel) {
        e.stopPropagation();
        onCancel();
      }
    },
  });
  const submit = async () => {
    if (!input.value.trim() || add.disabled) return;
    add.disabled = true;
    add.textContent = 'Looking up…';
    try {
      await onMusic(await resolveMusic(input.value));
    } catch (ex) {
      toast(ex.message);
      add.disabled = false;
      add.textContent = 'Add';
    }
  };
  add.addEventListener('click', submit);
  setTimeout(() => input.focus());
  return h('div', { class: 'row music-form' }, input, add);
}

// drawMusicArea shows the music attached to the post being written, or the
// box to paste a link into.
function drawMusicArea(area, toggle) {
  const f = chan();
  clearOutsideClose(area);
  const redraw = () => drawMusicArea(area, toggle);
  toggle.textContent = f.musicPicking ? 'Cancel' : '+ Link music';
  toggle.hidden = !!(f.music || f.playlist);
  if (f.music) {
    area.replaceChildren(musicChip(f.music, () => { f.music = null; redraw(); }));
    return;
  }
  if (f.playlist) {
    area.replaceChildren(playlistChip(f.playlist, () => { f.playlist = null; redraw(); }));
    return;
  }
  if (!f.musicPicking) {
    area.replaceChildren();
    return;
  }
  const close = () => { f.musicPicking = false; redraw(); };
  closeOnOutside(area, toggle, close);
  area.replaceChildren(musicLinkBox(
    m => { f.music = m; f.musicPicking = false; redraw(); },
    close,
    pl => { f.playlist = pl; f.musicPicking = false; redraw(); }));
}

// ---- the floating player ----

const player = {
  queue: [], // music, in play order
  index: -1, // what's playing (or paused); -1 = nothing yet
  collapsed: false,
  started: false, // whether a player has been loaded since the page opened
  engine: null, // the active YouTube player or Spotify controller
  engineKind: null,
  loadToken: 0, // bumps on every load, so stale callbacks know to stop
  endedAt: -1, // queue index we've already moved on from
  preview: false, // Spotify is only playing 30-second previews
  ytReady: null,
  volume: 80, // YouTube only; 0-100
};

// The parts of the widget that redraw, around an embed area that never moves
// (moving an iframe in the page reloads it and stops the music).
const musicBar = h('div', { class: 'win-bar music-bar' });
const musicEmbed = h('div', { class: 'music-embed' });
const musicInfo = h('div', { class: 'music-info' });
// The volume slider is built once, not redrawn, so a drag isn't cut short.
// Only YouTube's player lets a page set its volume; Spotify's embed doesn't.
const volumeSlider = h('input', { type: 'range', min: 0, max: 100, step: 1, 'aria-label': 'Volume' });
const volumeValue = h('span', { class: 'xs music-volume-value' });
const volumeNote = h('span', { class: 'xs muted' }, "Spotify's player has no volume control here; use your device's volume.");
const volumeLabel = h('span', { class: 'xs music-volume-label', 'aria-hidden': 'true' }, 'VOL');
const musicVolume = h('div', { class: 'music-volume', hidden: true }, volumeLabel, volumeSlider, volumeValue, volumeNote);
const musicEl = h('section', { class: 'win music', hidden: true, 'aria-label': 'Music player' }, musicBar, musicEmbed, musicVolume, musicInfo);
document.body.appendChild(musicEl);

// The queue survives a reload (in this browser), but doesn't start playing by itself.
try {
  const saved = JSON.parse(load('music') || 'null');
  if (saved && Array.isArray(saved.queue)) {
    player.queue = saved.queue;
    player.index = Math.min(saved.index ?? -1, saved.queue.length - 1);
    player.collapsed = !!saved.collapsed;
    if (Number.isFinite(saved.volume)) player.volume = Math.min(100, Math.max(0, saved.volume));
  }
} catch { /* empty queue */ }

function saveMusic() {
  store('music', JSON.stringify({ queue: player.queue, index: player.index, collapsed: player.collapsed, volume: player.volume }));
}

function showVolume() {
  volumeSlider.value = player.volume;
  volumeValue.textContent = `${player.volume}%`;
}

function setVolume(v) {
  player.volume = v;
  showVolume();
  if (player.engineKind === 'youtube' && player.engine) {
    try {
      player.engine.setVolume(v);
      if (v > 0 && player.engine.isMuted()) player.engine.unMute();
    } catch { /* player not ready yet; onReady applies it */ }
  }
  saveMusic();
}

volumeSlider.addEventListener('input', () => setVolume(Number(volumeSlider.value)));
// The wheel over the slider nudges it, a few percent a notch.
volumeSlider.addEventListener('wheel', e => {
  e.preventDefault();
  setVolume(Math.min(100, Math.max(0, player.volume + (e.deltaY < 0 ? 5 : -5))));
}, { passive: false });
showVolume();

function loadScript(src) {
  return new Promise((resolve, reject) => {
    const s = document.createElement('script');
    s.src = src;
    s.onload = resolve;
    s.onerror = reject;
    document.head.appendChild(s);
  });
}

// ytApi loads YouTube's player script the first time it's needed.
function ytApi() {
  if (window.YT && window.YT.Player) return Promise.resolve(window.YT);
  if (!player.ytReady) {
    player.ytReady = new Promise((resolve, reject) => {
      window.onYouTubeIframeAPIReady = () => resolve(window.YT);
      loadScript('https://www.youtube.com/iframe_api').catch(e => { player.ytReady = null; reject(e); });
    });
  }
  return player.ytReady;
}

function destroyEngine() {
  try { if (player.engine) player.engine.destroy(); } catch { /* already gone */ }
  player.engine = null;
  player.engineKind = null;
}

// finished moves on to the next item once the current one has played out.
function finished(token) {
  if (token !== player.loadToken || player.endedAt === player.index) return;
  player.endedAt = player.index;
  nextTrack();
}

async function loadYouTube(item, target, token) {
  const YT = await ytApi();
  if (token !== player.loadToken) return;
  const vars = { autoplay: 1, rel: 0, playsinline: 1, origin: location.origin };
  const opts = { host: 'https://www.youtube-nocookie.com', width: '100%', height: '100%', playerVars: vars };
  if (item.kind === 'playlist') {
    vars.listType = 'playlist';
    vars.list = item.extId;
  } else {
    opts.videoId = item.extId;
  }
  opts.events = {
    onReady: e => {
      e.target.setVolume(player.volume);
      if (player.volume > 0) e.target.unMute();
      e.target.playVideo();
    },
    onStateChange: e => {
      if (e.data !== YT.PlayerState.ENDED) return;
      // A YouTube playlist plays through by itself; move on after its last video.
      if (item.kind === 'playlist') {
        const list = e.target.getPlaylist() || [];
        if (e.target.getPlaylistIndex() < list.length - 1) return;
      }
      finished(token);
    },
    onError: e => {
      if (token !== player.loadToken) return;
      toast(e.data === 101 || e.data === 150 ? "That one can't be played outside YouTube; skipping it" : "YouTube couldn't play that; skipping it");
      setTimeout(() => finished(token), 1500);
    },
  };
  player.engine = new YT.Player(target, opts);
  player.engineKind = 'youtube';
}

// loadSpotify plays through spotify-player.html, which runs Spotify's embed
// controller (it needs eval, which this page doesn't allow) and passes its
// events back here.
function loadSpotify(item, target, token) {
  const uri = `spotify:${item.kind}:${item.extId}`;
  const frame = h('iframe', { src: `spotify-player.html#uri=${encodeURIComponent(uri)}`, title: `Spotify: ${item.title}`, allow: 'autoplay; encrypted-media; clipboard-write' });
  const cmd = c => frame.contentWindow && frame.contentWindow.postMessage({ cmd: c }, location.origin);
  let lastPosition = 0;
  const onMessage = e => {
    if (e.source !== frame.contentWindow || e.origin !== location.origin || !e.data || token !== player.loadToken) return;
    const { spotify: type, data } = e.data;
    if (type === 'ready') cmd('play');
    else if (type === 'error') embedFailed(item);
    else if (type === 'update') {
      const { isPaused, position, duration } = data;
      // Previews are always 30 seconds; full songs almost never are.
      const preview = duration >= 29000 && duration <= 31000;
      if (preview !== player.preview) {
        player.preview = preview;
        drawMusic();
      }
      // A single song that played to the end: move on. Albums and playlists
      // play through inside Spotify's player; ⏭ moves past them.
      const single = item.kind === 'track' || item.kind === 'episode';
      if (single && isPaused && duration > 0 && lastPosition >= duration - 1500) finished(token);
      lastPosition = position;
    }
  };
  window.addEventListener('message', onMessage);
  player.engine = {
    togglePlay: () => cmd('toggle'),
    destroy: () => { window.removeEventListener('message', onMessage); frame.remove(); },
  };
  player.engineKind = 'spotify';
  target.replaceWith(frame);
}

function embedFailed(item) {
  musicEmbed.replaceChildren(h('p', { class: 'music-fail xs' }, `Couldn't load ${providerName(item)}'s player. `,
    h('a', { href: safeUrl(item.url), target: '_blank', rel: 'noopener noreferrer' }, 'Open it instead ↗')));
}

// loadCurrent swaps the embed to the queue's current item and starts it.
async function loadCurrent() {
  const item = player.queue[player.index];
  const token = ++player.loadToken;
  destroyEngine();
  player.preview = false;
  musicEmbed.className = `music-embed ${item.provider}`;
  const target = h('div');
  musicEmbed.replaceChildren(target);
  try {
    if (item.provider === 'youtube') await loadYouTube(item, target, token);
    else loadSpotify(item, target, token);
  } catch {
    if (token === player.loadToken) embedFailed(item);
  }
}

function playIndex(i) {
  if (i < 0 || i >= player.queue.length) return;
  player.index = i;
  player.started = true;
  player.endedAt = -1;
  saveMusic();
  loadCurrent();
  drawMusic();
}

const nextTrack = () => { if (player.index < player.queue.length - 1) playIndex(player.index + 1); };
const prevTrack = () => { if (player.index > 0) playIndex(player.index - 1); };

function togglePlay() {
  const e = player.engine;
  if (!e) {
    playIndex(Math.max(player.index, 0));
    return;
  }
  if (player.engineKind === 'youtube') {
    if (e.getPlayerState() === 1) e.pauseVideo();
    else e.playVideo();
  } else {
    e.togglePlay();
  }
}

// playNow plays something straight away, keeping the rest of the queue after it.
function playNow(m) {
  player.queue.splice(player.index + 1, 0, m);
  playIndex(player.index + 1);
}

function queueAdd(m) {
  player.queue.push(m);
  if (player.queue.length === 1) {
    playIndex(0);
    return;
  }
  saveMusic();
  drawMusic();
  toast(`Queued ${m.title}`, 'ok');
}

// playAll replaces the queue with a list (a game's soundtrack, the Music page).
function playAll(list) {
  if (!list.length) return;
  player.queue = list.slice();
  playIndex(0);
}

function removeAt(i) {
  const wasCurrent = i === player.index;
  player.queue.splice(i, 1);
  if (!player.queue.length) {
    stopMusic();
    return;
  }
  if (i < player.index) player.index--;
  if (wasCurrent) {
    player.index = Math.min(player.index, player.queue.length - 1);
    if (player.started) {
      playIndex(player.index);
      return;
    }
  }
  saveMusic();
  drawMusic();
}

function stopMusic() {
  player.loadToken++;
  destroyEngine();
  musicEmbed.replaceChildren();
  player.queue = [];
  player.index = -1;
  player.started = false;
  saveMusic();
  drawMusic();
}

function spotifyHint() {
  const reload = h('button', { class: 'btn sm', type: 'button', onclick: () => playIndex(player.index) }, "I've logged in: reload");
  const login = h('a', {
    class: 'btn sm primary', href: SPOTIFY_LOGIN, target: '_blank', rel: 'noopener noreferrer',
    title: 'Some browsers (like Safari) hide your Spotify login from players on other sites; if so, use "Open in Spotify".',
  }, 'Log in to Spotify');
  if (player.preview) {
    return h('div', { class: 'music-hint' },
      h('strong', {}, 'Only hearing 30-second previews?'),
      h('span', { class: 'xs' }, " Spotify plays full songs when you're logged in to it in this browser."),
      h('div', { class: 'row' }, login, reload));
  }
  return h('p', { class: 'xs muted music-login' }, 'Not hearing full songs? ',
    h('a', { href: SPOTIFY_LOGIN, target: '_blank', rel: 'noopener noreferrer' }, 'Log in to Spotify'),
    ' in this browser, then ',
    h('button', { class: 'link xs', type: 'button', onclick: () => playIndex(player.index) }, 'reload the player'), '.');
}

// drawMusic redraws the widget's bar and info; the embed is left alone.
function drawMusic() {
  musicEl.hidden = !player.queue.length;
  if (!player.queue.length) return;
  musicEl.className = `win music music-${state.feedSide === 'right' ? 'left' : 'right'}${player.collapsed ? ' collapsed' : ''}`;
  const item = player.queue[player.index] || player.queue[0];
  const ctl = (label, title, fn, disabled) => h('button', { class: 'music-ctl', type: 'button', title, 'aria-label': title, disabled, onclick: fn }, label);
  musicBar.replaceChildren(
    h('button', { class: 'win-close', type: 'button', title: 'Close the player and clear the queue', 'aria-label': 'Close player', onclick: stopMusic }),
    h('span', { class: 'win-title music-title', title: item.title }, `♪ ${player.started ? item.title : 'Music'}`),
    ctl('⏮', 'Previous', prevTrack, player.index <= 0),
    ctl(player.started ? '⏯' : '▶', player.started ? 'Play / pause' : 'Play', togglePlay),
    ctl('⏭', 'Next', nextTrack, player.index >= player.queue.length - 1),
    ctl(player.collapsed ? '▴' : '▾', player.collapsed ? 'Expand' : 'Collapse', () => {
      player.collapsed = !player.collapsed;
      saveMusic();
      drawMusic();
    }));

  const now = player.started && player.queue[player.index];
  musicVolume.hidden = !now;
  const youtube = now && now.provider === 'youtube';
  volumeLabel.hidden = !youtube;
  volumeSlider.hidden = !youtube;
  volumeValue.hidden = !youtube;
  volumeNote.hidden = youtube;
  musicInfo.replaceChildren(...[
    now ? null : h('button', { class: 'btn primary block', type: 'button', onclick: () => playIndex(Math.max(player.index, 0)) }, `▶ Play ${player.queue.length > 1 ? 'the queue' : item.title}`),
    now && now.provider === 'spotify' ? spotifyHint() : null,
    now ? h('div', { class: 'music-links xs' },
      h('a', { href: safeUrl(now.url), target: '_blank', rel: 'noopener noreferrer' }, `Open in ${providerName(now)} ↗`),
      ' · Find on ', alsoLinks(now)) : null,
    h('div', { class: 'row between music-queue-head' },
      h('h4', {}, `Queue (${player.queue.length})`),
      h('button', { class: 'link xs danger', type: 'button', onclick: stopMusic }, 'Clear')),
    h('ol', { class: 'music-queue' }, player.queue.map((m, i) => h('li', { class: i === player.index && player.started ? 'on' : null },
      h('button', { class: 'music-q-title', type: 'button', title: `Play ${m.title}`, onclick: () => playIndex(i) },
        h('span', {}, m.title), h('span', { class: 'muted xs' }, ` · ${MUSIC_KINDS[m.kind] || ''}, ${providerName(m)}`)),
      h('button', { class: 'link xs danger', type: 'button', title: 'Remove from queue', 'aria-label': `Remove ${m.title}`, onclick: () => removeAt(i) }, '×')))),
  ].filter(Boolean));
}

drawMusic();

// ---- the Music page ----

async function renderMusicPage() {
  const shares = await pageData('/music/shares');
  const people = [...new Set(shares.map(s => s.username).filter(Boolean))].sort();
  const who = h('select', { 'aria-label': 'Shared by' }, h('option', { value: '' }, 'Everyone'), people.map(p => h('option', { value: p }, p)));
  const text = h('input', { type: 'search', placeholder: 'Filter by song, artist or game…', 'aria-label': 'Filter music' });
  const list = h('div', { class: 'music-shares' });
  let shown = [];

  const where = s => {
    if (s.where === 'game') {
      return ['on ', h('a', { href: `#/board/${s.boardId}`, onclick: () => { state.pendingGame = s.gameId; } }, s.gameTitle), ` (${s.boardName})`];
    }
    return s.boardId ? ['in ', h('a', { href: `#/board/${s.boardId}` }, s.boardName), "'s chat"] : ['in chat'];
  };
  const draw = () => {
    const q = text.value.trim().toLowerCase();
    shown = shares.filter(s => (!who.value || s.username === who.value)
      && (!q || [s.music.title, s.music.artist, s.gameTitle].join(' ').toLowerCase().includes(q)));
    list.replaceChildren(...(shown.length
      ? shown.map(s => h('div', { class: 'music-share' },
        h('div', { class: 'muted xs' }, avatar(s.userId, s.username, 'none tiny'), ` ${s.username || 'someone'} shared `, where(s), ` · ${ago(s.at)}`),
        musicChip(s.music)))
      : [h('p', { class: 'muted' }, shares.length ? 'Nothing matches that.' : 'No music shared yet. Use "+ Link music" in the chat, or add a soundtrack to a game.')]));
  };
  who.addEventListener('change', draw);
  text.addEventListener('input', draw);

  shell(
    musicHead('shared'),
    win('Shared Music', { cls: 'music-page' },
      h('div', { class: 'row' }, who, text,
        h('button', {
          class: 'btn primary', type: 'button',
          onclick: () => {
            // Each song once, in the order shown.
            const seen = new Set();
            playAll(shown.map(s => s.music).filter(m => !seen.has(m.id) && seen.add(m.id)));
          },
        }, '▶ Play all shown')),
      list));
  draw();
}

// musicHead is the Music pages' heading, with tabs for shared music and playlists.
function musicHead(tab) {
  const tabLink = (key, href, label) => h('a', { class: 'music-tab' + (tab === key ? ' on' : ''), href, 'aria-current': tab === key ? 'page' : null }, label);
  return h('div', { class: 'page-head' },
    h('h1', {}, 'Music'),
    h('nav', { class: 'music-tabs', 'aria-label': 'Music' },
      tabLink('shared', '#/music', 'Shared'),
      tabLink('playlists', '#/music/playlists', 'Playlists')),
    h('p', { class: 'muted' }, tab === 'shared'
      ? 'Everything shared in the chats and on games, newest first.'
      : 'Mix songs from Spotify and YouTube into playlists, then share them in chat or put them on games.'));
}

async function renderPlaylistsPage() {
  const list = state.playlists = await pageData('/playlists');
  const mine = myPlaylists(list);
  const others = list.filter(p => p.userId !== state.user.id);

  const name = h('input', { placeholder: 'Playlist name, e.g. "Valheim boss fights"', maxlength: 60, required: true, 'aria-label': 'Playlist name' });
  const create = h('form', {
    class: 'row',
    onsubmit: async e => {
      e.preventDefault();
      try {
        const pl = await api('POST', '/playlists', { name: name.value });
        location.hash = `#/music/playlist/${pl.id}`;
      } catch (ex) { toast(ex.message); }
    },
  }, name, h('button', { class: 'btn primary', type: 'submit' }, 'Create'));

  const section = (title, items, empty) => [
    h('h3', { class: 'music-section' }, title),
    items.length ? h('div', { class: 'stack music-list' }, items.map(pl => playlistChip(pl))) : h('p', { class: 'muted xs' }, empty),
  ];
  shell(
    musicHead('playlists'),
    win('Playlists', { cls: 'music-page' },
      h('div', { class: 'field' }, h('span', {}, 'New playlist'), create),
      section('Your playlists', mine, 'None yet. Make one above, or use ♫+ on any song.'),
      section("Everyone else's", others, 'Nobody else has made one yet.')));
}

async function renderPlaylistPage(id) {
  const first = await pageData(`/playlists/${id}`);
  const root = h('div', { class: 'playlist-page' });
  shell(musicHead('playlists'), root);
  const draw = pl => {
    const mine = pl.userId === state.user.id;
    const canDelete = mine || (state.user.isAdmin && state.user.adminMode);
    const act = async fn => {
      try { draw(await fn()); } catch (e) { toast(e.message); }
    };
    const move = (i, by) => {
      const ids = pl.items.map(m => m.id);
      [ids[i], ids[i + by]] = [ids[i + by], ids[i]];
      act(() => api('PUT', `/playlists/${pl.id}/items`, { musicIds: ids }));
    };
    const rename = () => {
      const input = h('input', { value: pl.name, maxlength: 60, 'aria-label': 'Playlist name' });
      const save = () => act(() => api('PATCH', `/playlists/${pl.id}`, { name: input.value }));
      input.addEventListener('keydown', e => {
        if (e.key === 'Enter') save();
        else if (e.key === 'Escape') draw(pl);
      });
      title.replaceWith(h('div', { class: 'row' }, input,
        h('button', { class: 'btn sm primary', type: 'button', onclick: save }, 'Save'),
        h('button', { class: 'btn sm ghost', type: 'button', onclick: () => draw(pl) }, 'Cancel')));
      input.focus();
      input.select();
    };
    const del = async () => {
      if (!confirm(`Delete "${pl.name}"? Chat posts and games that link it will lose it.`)) return;
      try {
        await api('DELETE', `/playlists/${pl.id}`);
        toast(`Deleted ${pl.name}`, 'ok');
        location.hash = '#/music/playlists';
      } catch (e) { toast(e.message); }
    };

    const title = h('h2', { class: 'playlist-name' }, pl.name);
    const n = pl.items.length;
    root.replaceChildren(win('Playlist', { cls: 'music-page' },
      h('p', { class: 'xs' }, h('a', { href: '#/music/playlists' }, '◀ All playlists')),
      title,
      h('p', { class: 'muted xs' }, `by ${pl.username} · ${songCount(n)} · updated ${ago(pl.updatedAt)}`),
      h('div', { class: 'row' },
        h('button', { class: 'btn primary', type: 'button', disabled: !n, onclick: () => playAll(pl.items) }, '▶ Play'),
        h('button', { class: 'btn', type: 'button', disabled: !n, onclick: () => queuePlaylist(pl) }, '+ Queue all'),
        h('span', { class: 'spacer' }),
        mine ? h('button', { class: 'btn sm', type: 'button', onclick: rename }, 'Rename') : null,
        canDelete ? h('button', { class: 'btn sm danger', type: 'button', onclick: del }, 'Delete') : null),
      mine ? h('div', { class: 'field' }, h('span', {}, 'Add a song'),
        musicLinkBox(m => act(() => api('POST', `/playlists/${pl.id}/items`, { musicId: m.id })))) : null,
      n
        ? h('ol', { class: 'playlist-items' }, pl.items.map((m, i) => h('li', {},
          musicChip(m, mine ? () => act(() => api('DELETE', `/playlists/${pl.id}/items/${m.id}`)) : null, {
            // ▶ on a song plays the playlist from there.
            onPlay: () => playAll(pl.items.slice(i)),
            extra: mine ? [
              h('button', { class: 'btn sm', type: 'button', title: 'Move up', 'aria-label': `Move ${m.title} up`, disabled: i === 0, onclick: () => move(i, -1) }, '↑'),
              h('button', { class: 'btn sm', type: 'button', title: 'Move down', 'aria-label': `Move ${m.title} down`, disabled: i === n - 1, onclick: () => move(i, 1) }, '↓'),
            ] : null,
          }))))
        : h('p', { class: 'muted' }, mine
          ? 'No songs yet. Paste a link above, or use ♫+ on any song in the chat or on the Shared tab.'
          : 'No songs yet.')));
  };
  const load = async () => draw(await api('GET', `/playlists/${id}`));
  // Adding a song here with ♫+ (e.g. to this very playlist) shows straight away.
  state.onPlaylistChange = changed => { if (changed.id === id) load().catch(e => toast(e.message)); };
  draw(first);
}

// ---------- ticker ----------

async function loadTicker() {
  try {
    state.ticker = await api('GET', '/ticker');
    state.tickerAt = Date.now();
    drawTicker();
  } catch { /* keep whatever is showing */ }
}

// drawTicker fills the footer's scrolling line, slowing it down as it gets longer.
function drawTicker() {
  const el = document.getElementById('ticker');
  if (!el || !state.ticker) return;
  const text = state.ticker.items.length ? `★ ${state.ticker.items.join(' ★ ')} ★` : '';
  const span = h('span', {}, text);
  span.style.animationDuration = `${Math.max(20, Math.round(text.length * 0.19))}s`;
  el.replaceChildren(span);
}

function openTickerModal() {
  const root = h('div', { class: 'stack' });
  const draw = () => {
    const t = state.ticker;
    if (!t) {
      root.replaceChildren(h('p', { class: 'muted' }, 'Loading…'));
      return;
    }
    const resets = new Date(t.resetsAt * 1000).toLocaleString([], { weekday: 'long', hour: 'numeric', minute: '2-digit' });
    const input = h('input', { maxlength: t.maxLength, placeholder: 'Say something to the group…', 'aria-label': 'Ticker message' });
    const full = t.mine.length >= t.perUser;
    const form = h('form', {
      class: 'row',
      onsubmit: async e => {
        e.preventDefault();
        if (!input.value.trim()) return;
        try {
          state.ticker = await api('POST', '/ticker', { text: input.value });
          drawTicker();
          draw();
        } catch (ex) { toast(ex.message); }
      },
    }, input, h('button', { class: 'btn primary', type: 'submit', disabled: full }, 'Add'));
    root.replaceChildren(...[
      h('p', { class: 'muted xs' },
        `Add up to ${t.perUser} messages (${t.maxLength} characters each) to the scrolling line at the bottom. `
        + `Everyone's messages are cleared at the weekly reset (${resets}) and it goes back to the defaults.`),
      full ? h('p', { class: 'xs' }, `You've used all ${t.perUser}. Delete one to add another.`) : form,
      t.mine.length ? h('div', { class: 'admin-list' }, t.mine.map(m => h('div', { class: 'admin-row' },
        h('span', { class: 'grow' }, m.text),
        h('button', {
          class: 'btn sm danger',
          onclick: async () => {
            try {
              state.ticker = await api('DELETE', `/ticker/${m.id}`);
              drawTicker();
              draw();
            } catch (ex) { toast(ex.message); }
          },
        }, 'Delete')))) : h('p', { class: 'muted xs' }, "You haven't added anything this week."),
    ].filter(Boolean));
    if (!full) input.focus();
  };
  openModal('The Ticker', root);
  draw();
  if (!state.ticker) loadTicker().then(draw);
}

function openModal(title, content, wide) {
  closeModal();
  const box = win(title, { cls: 'modal' + (wide ? ' wide' : ''), onClose: closeModal }, content);
  box.setAttribute('role', 'dialog');
  box.setAttribute('aria-modal', 'true');
  const overlay = h('div', { class: 'overlay', onmousedown: e => { if (e.target === overlay) closeModal(); } }, box);
  document.body.appendChild(overlay);
  document.body.classList.add('no-scroll');
  state.modal = overlay;
}

function closeModal() {
  if (!state.modal) return;
  state.modal.remove();
  state.modal = null;
  document.body.classList.remove('no-scroll');
  // A dialog can ask to tidy up after itself (e.g. undo a theme preview).
  const after = state.onModalClose;
  state.onModalClose = null;
  if (after) after();
}

// ---------- boards list ----------

async function renderBoards() {
  const boards = await pageData('/boards');
  state.boards = boards;
  state.board = null;
  state.games = [];

  const name = h('input', { placeholder: 'Board name, e.g. "Co-op nights"', maxlength: 100, required: true });
  const desc = h('input', { placeholder: 'Description (optional)', maxlength: 1000 });
  const personal = h('input', { type: 'checkbox' });
  const form = h('form', {
    class: 'stack',
    onsubmit: async e => {
      e.preventDefault();
      try {
        const b = await api('POST', '/boards', { name: name.value, description: desc.value, personal: personal.checked });
        location.hash = `#/board/${b.id}`;
      } catch (ex) { toast(ex.message); }
    },
  }, name, desc,
  h('label', { class: 'check', title: 'Others can still see it' }, personal, 'Personal board (nobody can request to join)'),
  h('button', { class: 'btn primary', type: 'submit' }, 'Create board'));

  const mine = boards.filter(isMyBoard);
  const others = boards.filter(b => !isMyBoard(b));
  shell(
    h('div', { class: 'page-head' },
      h('h1', {}, 'Boards'),
      h('p', { class: 'muted' }, "Everyone can see every board. Only a board's owner and the editors they add can change it.")),
    h('h2', { class: 'section-title' }, 'My boards'),
    h('div', { class: 'board-grid' }, mine.map(boardCard), win('New Board', { cls: 'new-board' }, form)),
    h('h2', { class: 'section-title' }, 'Other boards'),
    others.length
      ? h('div', { class: 'board-grid' }, others.map(boardCard))
      : h('p', { class: 'section-empty' }, 'No other boards yet.'));
}

// isMyBoard is true for boards the user owns or was added to as an editor
// (not boards an admin can edit only because they're an admin).
function isMyBoard(b) {
  return b.createdById === state.user.id || b.members.some(m => m.id === state.user.id);
}

function boardCard(b) {
  const open = () => { location.hash = `#/board/${b.id}`; };
  let tag = null;
  if (b.requests.length) tag = h('span', { class: 'pill hot' }, `${b.requests.length} join request${b.requests.length === 1 ? '' : 's'}`);
  else if (b.personal) tag = h('span', { class: 'pill' }, 'personal');
  else if (!b.canEdit) tag = h('span', { class: 'pill' }, b.requested ? 'request sent' : 'view only');
  const canRequest = !b.canEdit && !b.requested && !b.personal;

  // The card is clickable rather than a link, so it can hold a button.
  return h('div', {
    class: 'board-link', role: 'link', tabindex: '0', onclick: open,
    onkeydown: e => { if (e.key === 'Enter' && e.target === e.currentTarget) open(); },
  },
  win(b.name, { cls: 'board-card' },
    h('div', { class: 'row between' }, h('div', { class: 'folder', 'aria-hidden': 'true' }), tag),
    b.description ? h('p', { class: 'clamp' }, b.description) : null,
    editorsLine(b),
    !canRequest ? null : h('div', {}, h('button', {
      class: 'btn sm',
      onclick: async e => {
        e.stopPropagation();
        if (await requestJoin(b.id)) renderBoards();
      },
    }, 'Request to join')),
    h('div', { class: 'board-meta' },
      `${b.gameCount} game${b.gameCount === 1 ? '' : 's'} · updated ${ago(b.updatedAt)}`)));
}

// requestJoin asks a board's owner for editor access and returns the updated board.
async function requestJoin(boardId) {
  try {
    const b = await api('POST', `/boards/${boardId}/requests`);
    toast(`Request sent to ${b.createdBy || 'the admins'}`, 'ok');
    return b;
  } catch (e) {
    toast(e.message);
    return null;
  }
}

// editorsLine lists who can edit a board: its owner, then the editors they added.
function editorsLine(b) {
  const names = [b.createdBy ? `${b.createdBy} (owner)` : null, ...b.members.map(m => m.username)].filter(Boolean);
  return h('div', { class: 'editors' }, h('b', {}, 'Editors: '), names.length ? names.join(', ') : 'nobody yet');
}

// ---------- admin ----------

async function renderAdmin() {
  if (!state.user.isAdmin) {
    shell(h('div', { class: 'empty' }, win('Access Denied', { cls: 'alert' },
      h('p', {}, '⚠ Admins only.'),
      h('a', { href: '#/', class: 'btn' }, 'Back to boards'))));
    return;
  }
  const [pending, users, ticker] = await Promise.all([
    api('GET', '/admin/pending'), api('GET', '/admin/users'), api('GET', '/admin/ticker'),
  ]);

  // Run an admin action, then redraw the page. A returned password is shown once.
  const act = async (fn, secretTitle, username) => {
    try {
      const d = await fn();
      if (d && d.password) state.adminSecret = { title: secretTitle, username, password: d.password };
      await renderAdmin();
    } catch (e) { toast(e.message); }
  };

  let secret = null;
  if (state.adminSecret) {
    const s = state.adminSecret;
    const pw = h('code', { class: 'pw' }, s.password);
    secret = win(s.title, { cls: 'secret', onClose: () => { state.adminSecret = null; secret.remove(); } },
      h('p', { class: 'xs' }, `Password for ${s.username}. Send it to them privately; it won't be shown again.`),
      h('div', { class: 'row' }, pw, h('button', {
        class: 'btn sm', onclick: async () => {
          try {
            await navigator.clipboard.writeText(s.password);
            toast('Copied', 'ok');
          } catch {
            getSelection().selectAllChildren(pw);
            toast('Press Ctrl+C to copy', 'ok');
          }
        },
      }, 'Copy')));
  }

  const pendingList = pending.length
    ? h('div', { class: 'admin-list' }, pending.map(p => h('div', { class: 'admin-row' },
      h('div', { class: 'grow' },
        h('strong', {}, p.username),
        h('span', { class: 'muted xs' }, ` requested ${ago(p.createdAt)}`),
        p.message ? h('div', { class: 'admin-msg' }, p.message) : null),
      h('button', { class: 'btn sm primary', onclick: () => act(() => api('POST', `/admin/pending/${p.id}/approve`)) }, 'Approve'),
      h('button', {
        class: 'btn sm danger', onclick: () => {
          if (confirm(`Reject the request from ${p.username}?`)) act(() => api('DELETE', `/admin/pending/${p.id}`));
        },
      }, 'Reject'))))
    : h('p', { class: 'muted' }, 'No pending requests.');

  const newName = h('input', { placeholder: 'Username', required: true, minlength: 3, maxlength: 32, autocomplete: 'off' });
  const newPw = h('input', { type: 'password', placeholder: 'Password (leave blank to generate one)', maxlength: 72, autocomplete: 'new-password' });
  const createForm = h('form', {
    class: 'row',
    onsubmit: e => {
      e.preventDefault();
      const name = newName.value.trim();
      act(() => api('POST', '/admin/users', { username: name, password: newPw.value }).then(d => {
        toast(`Created ${name}`, 'ok');
        return d;
      }), 'Account created', name);
    },
  }, newName, newPw, h('button', { class: 'btn primary', type: 'submit' }, 'Create account'));

  const userList = h('div', { class: 'admin-list' }, users.map(x => {
    const isMe = x.id === state.user.id;
    const managed = !isMe && !x.isAdmin;
    // Any admin can take down anyone's picture, including other admins'.
    const removePicture = avatarUrl(x.id) ? h('button', {
      class: 'btn sm', onclick: () => {
        if (confirm(`Remove ${x.username}'s profile picture?`)) {
          act(async () => {
            await api('DELETE', `/admin/users/${x.id}/avatar`);
            delete state.avatars[x.id];
          });
        }
      },
    }, 'Remove picture') : null;
    return h('div', { class: 'admin-row' },
      avatar(x.id, x.username, 'none big'),
      h('div', { class: 'grow' },
        h('strong', {}, x.username),
        x.isAdmin ? h('span', { class: 'pill' }, 'admin') : null,
        isMe ? h('span', { class: 'pill' }, 'you') : null,
        h('div', { class: 'muted xs' }, `joined ${ago(x.createdAt)} · ${x.lastLoginAt ? 'last login ' + ago(x.lastLoginAt) : 'never logged in'}`)),
      removePicture,
      managed ? [
        h('button', {
          class: 'btn sm', onclick: () => {
            if (confirm(`Reset ${x.username}'s password? They'll be logged out everywhere.`)) {
              act(() => api('POST', `/admin/users/${x.id}/reset-password`), 'Password reset', x.username);
            }
          },
        }, 'Reset password'),
        h('button', {
          class: 'btn sm danger', onclick: () => {
            if (confirm(`Remove ${x.username}? They lose access immediately. Their votes are deleted; their notes stay, marked "deleted user".`)) {
              act(() => api('DELETE', `/admin/users/${x.id}`));
            }
          },
        }, 'Remove'),
      ] : h('span', { class: 'muted xs' }, isMe ? '' : 'managed from server CLI'));
  }));

  shell(
    h('div', { class: 'page-head' },
      h('h1', {}, 'Admin Control Panel'),
      h('p', { class: 'muted' }, 'Approve sign-ups and manage accounts. Admin accounts themselves are created on the server with ', h('code', {}, 'gameboard create-admin'), '.')),
    h('div', { class: 'admin-page' },
      secret,
      win(`Pending Requests (${pending.length})`, {}, pendingList),
      win('Create an Account', {}, createForm),
      win(`Users (${users.length})`, {}, userList),
      win('Ticker', {}, tickerAdmin(ticker, act))));
}

// tickerAdmin edits the ticker's default messages and moderates people's ones.
function tickerAdmin(t, act) {
  // Any change here should show up in the footer straight away.
  const change = fn => act(async () => {
    await fn();
    state.ticker = null;
  });
  const resets = new Date(t.resetsAt * 1000).toLocaleString([], { weekday: 'long', day: 'numeric', month: 'short', hour: 'numeric', minute: '2-digit' });

  const defaults = t.defaults.map(d => h('div', { class: 'row' },
    h('input', {
      value: d.text, maxlength: 80, 'aria-label': 'Default message',
      onchange: e => change(() => api('PATCH', `/admin/ticker/defaults/${d.id}`, { text: e.target.value })),
    }),
    h('button', { class: 'btn sm danger', onclick: () => change(() => api('DELETE', `/admin/ticker/defaults/${d.id}`)) }, 'Remove')));
  const newDefault = h('input', { maxlength: 80, placeholder: 'New default message', 'aria-label': 'New default message' });
  const addForm = h('form', {
    class: 'row',
    onsubmit: e => {
      e.preventDefault();
      if (newDefault.value.trim()) change(() => api('POST', '/admin/ticker/defaults', { text: newDefault.value }));
    },
  }, newDefault, h('button', { class: 'btn primary sm', type: 'submit' }, 'Add default'));

  const messages = t.messages.length
    ? h('div', { class: 'admin-list' }, t.messages.map(m => h('div', { class: 'admin-row' },
      h('div', { class: 'grow' }, m.text, h('div', { class: 'muted xs' }, `${m.username} · ${ago(m.createdAt)}`)),
      h('button', { class: 'btn sm danger', onclick: () => change(() => api('DELETE', `/ticker/${m.id}`)) }, 'Remove'))))
    : h('p', { class: 'muted xs' }, 'Nobody has added anything this week.');

  return h('div', { class: 'stack' },
    h('h4', {}, 'Defaults'),
    h('p', { class: 'muted xs' }, 'Always on the ticker. Edits save when you leave the box.'),
    ...defaults,
    addForm,
    h('h4', {}, "This week's messages from people"),
    h('p', { class: 'muted xs' }, `Cleared automatically ${resets}.`),
    messages,
    t.messages.length ? h('div', {}, h('button', {
      class: 'btn sm danger',
      onclick: () => { if (confirm("Clear everyone's ticker messages now?")) change(() => api('DELETE', '/admin/ticker/messages')); },
    }, 'Clear all now')) : null);
}

// ---------- board view ----------

async function renderBoard(id) {
  const cached = boardCache.get(id);
  if (cached && !(state.board && state.board.id === id)) {
    state.board = cached.board;
    state.columns = cached.columns;
    state.games = cached.games;
    state.boardEtag = cached.etag;
  }
  const have = state.board && state.board.id === id;
  if (!have) await loadBoard(id);
  drawBoard();
  startPolling(id);
  if (state.pendingGame) {
    const gameId = state.pendingGame;
    state.pendingGame = null;
    openGame(gameId);
  }
  if (!have) return;
  // Drawn from what we had: now check with the server ("changed?" costs
  // almost nothing when it hasn't).
  const seq = routeSeq;
  const before = JSON.stringify(state.board);
  try {
    if (!(await loadBoard(id)) || seq !== routeSeq) return;
    if (!state.dragging) drawColumns();
    // Name, editors and permissions are in the header: redraw that too.
    if (JSON.stringify(state.board) !== before) refreshPage(seq);
  } catch {
    // Gone, or no longer visible to us: show that properly.
    if (seq !== routeSeq) return;
    boardCache.delete(id);
    state.board = null;
    route();
  }
}

// loadBoard fetches a board, its columns and games. It returns true if it got
// new data, false if nothing changed since last time or the user has moved
// on to another page.
async function loadBoard(id) {
  // Only ask "has it changed?" if we're still holding this board's data.
  const have = state.board && state.board.id === id ? state.boardEtag : null;
  const r = await apiPoll(`/boards/${id}`, have);
  const m = location.hash.match(/^#\/board\/(\d+)/);
  if (!m || Number(m[1]) !== id || !r) return false;
  state.board = r.data.board;
  state.columns = r.data.columns;
  state.games = r.data.games;
  state.boardEtag = r.etag;
  return true;
}

function startPolling(id) {
  stopPolling();
  state.pollTimer = setInterval(async () => {
    if (document.hidden || state.dragging) return;
    try {
      if (await loadBoard(id)) drawColumns();
    } catch { /* try again next tick */ }
  }, 15000);
}

function stopPolling() {
  clearInterval(state.pollTimer);
  state.pollTimer = null;
}

function drawBoard() {
  const b = state.board;
  const sortSel = h('select', {
    'aria-label': 'Sort',
    onchange: e => { state.sort = e.target.value; store('sort', state.sort); drawColumns(); },
  }, SORTS.map(([v, l]) => h('option', { value: v }, l)));
  sortSel.value = state.sort;

  shell(
    win(b.name, { cls: 'board-head', onClose: () => { location.hash = '#/'; } },
      h('div', { class: 'board-title' },
        h('a', { href: '#/', class: 'crumb' }, '◀ All boards'),
        b.description ? h('p', {}, b.description) : null,
        editorsLine(b),
        boardNotice(b)),
      h('div', { class: 'board-actions' },
        h('input', {
          type: 'search', placeholder: 'Filter games…', value: state.filter, 'aria-label': 'Filter games',
          oninput: e => { state.filter = e.target.value; drawColumns(); },
        }),
        sortSel,
        b.canEdit ? [
          h('button', { class: 'btn', onclick: () => openColumnsEditor(false) }, 'Edit columns'),
          h('button', { class: 'btn', onclick: openEditBoard }, 'Edit board'),
          h('button', { class: 'btn primary', onclick: openAddGame }, '+ Add game'),
        ] : null)),
    h('div', { class: 'columns', id: 'columns' }));
  document.querySelector('.main').classList.add('board-main');
  sideScrollOnHeaders(document.getElementById('columns'));
  drawColumns();
}

// sideScrollOnHeaders makes the mouse wheel scroll the columns sideways while
// the pointer is over a column's title bar, the scrollbar along the bottom
// (or the gaps between columns), or the "+ New column" tile. Over a column's
// cards it scrolls that column as usual.
function sideScrollOnHeaders(cols) {
  cols.addEventListener('wheel', e => {
    const sideways = e.target === cols || e.target.closest('.column .win-bar, .add-col');
    if (!sideways) return;
    if (Math.abs(e.deltaX) > Math.abs(e.deltaY)) return; // already scrolling sideways
    e.preventDefault();
    const step = e.deltaMode === 1 ? 40 : e.deltaMode === 2 ? cols.clientWidth : 1; // lines / pages / pixels
    cols.scrollLeft += e.deltaY * step;
  }, { passive: false });
}

// boardNotice is the yellow note under a board's title: join controls for
// viewers, and waiting requests for the people who can approve them.
function boardNotice(b) {
  const update = async fn => {
    const next = await fn();
    if (next) {
      state.board = next;
      drawBoard();
    }
  };
  const asAdmin = state.user.adminMode && !isMyBoard(b);
  if (b.canEdit) {
    const notes = [];
    if (asAdmin) {
      notes.push(h('div', { class: 'view-only admin-note' },
        "You're editing this board as an admin. ",
        h('button', { class: 'link', onclick: () => setAdminMode(false) }, 'Switch to view mode')));
    }
    if (b.canManage && b.requests.length) {
      const n = b.requests.length;
      notes.push(h('div', { class: 'view-only' },
        `${b.requests.map(r => r.username).join(', ')} ${n === 1 ? 'wants' : 'want'} to join. `,
        h('button', { class: 'link', onclick: openEditBoard }, 'Review')));
    }
    return notes.length ? notes : null;
  }
  // Admins in view mode get a quick way back into edit mode.
  const adminSwitch = state.user.isAdmin
    ? h('button', { class: 'link', onclick: () => setAdminMode(true) }, 'Edit as admin')
    : null;
  if (b.requested) {
    return h('div', { class: 'view-only' },
      `Join request sent; waiting for ${b.createdBy || 'an admin'} to approve. `,
      h('button', {
        class: 'link',
        onclick: () => update(async () => {
          try { return await api('DELETE', `/boards/${b.id}/requests/${state.user.id}`); } catch (e) { toast(e.message); return null; }
        }),
      }, 'Cancel request'),
      adminSwitch);
  }
  if (b.personal) {
    return h('div', { class: 'view-only' },
      `${b.createdBy || 'Someone'}'s personal board: view only. You can still vote and leave notes. `, adminSwitch);
  }
  return h('div', { class: 'view-only' },
    'View only: you can vote and leave notes. ',
    h('button', { class: 'btn sm', onclick: () => update(() => requestJoin(b.id)) }, 'Request to join'),
    adminSwitch);
}

function matchesFilter(g, f) {
  if (!f) return true;
  const m = g.meta || {};
  const hay = [g.title, ...(m.genres || []), ...(m.modes || []), ...(m.developers || [])].join(' ').toLowerCase();
  return hay.includes(f);
}

function sortGames(list) {
  const cmp = {
    manual: (a, b) => a.position - b.position,
    wanted: (a, b) => interestScore(b) - interestScore(a) || a.position - b.position,
    name: (a, b) => a.title.localeCompare(b.title),
    price: (a, b) => priceCents(a) - priceCents(b) || a.title.localeCompare(b.title),
    added: (a, b) => b.createdAt - a.createdAt,
  }[state.sort] || ((a, b) => a.position - b.position);
  return [...list].sort(cmp);
}

function drawColumns() {
  const wrap = document.getElementById('columns');
  if (!wrap) return;
  const f = state.filter.trim().toLowerCase();
  const games = state.games.filter(g => matchesFilter(g, f));
  const editable = state.board.canEdit;
  const cols = state.columns.map(c => {
    const list = sortGames(games.filter(g => g.columnId === c.id));
    const body = h('div', { class: 'col-body', dataset: { column: c.id } },
      list.length ? list.map(gameCard) : h('div', { class: 'col-empty' }, editable ? 'Drop games here' : 'No games yet'));
    if (editable) attachDrop(body);
    return win(c.name, { cls: `column col-c-${c.color}`, extra: h('span', { class: 'count' }, String(list.length)) }, body);
  });
  if (editable) cols.push(h('button', { class: 'add-col', onclick: () => openColumnsEditor(true) }, '+ New column'));
  wrap.replaceChildren(...cols);
}

// openColumnsEditor lets people add, rename, recolour, reorder and delete the
// board's columns. Every change saves immediately.
function openColumnsEditor(focusNew) {
  const root = h('div', { class: 'stack' });

  const run = async fn => {
    try {
      await fn();
      if (await loadBoard(state.board.id)) drawColumns();
      draw();
    } catch (e) { toast(e.message); }
  };

  const columnRow = (c, i, count) => {
    const name = h('input', {
      value: c.name, maxlength: 40, 'aria-label': 'Column name',
      onchange: e => run(() => api('PATCH', `/columns/${c.id}`, { name: e.target.value })),
    });
    const swatches = h('div', { class: 'swatches' }, COLORS.map(col => h('button', {
      type: 'button', class: `swatch col-c-${col}${c.color === col ? ' on' : ''}`, title: col, 'aria-label': col,
      onclick: () => run(() => api('PATCH', `/columns/${c.id}`, { color: col })),
    })));
    const move = dir => {
      const ids = state.columns.map(x => x.id);
      [ids[i], ids[i + dir]] = [ids[i + dir], ids[i]];
      run(() => api('PUT', `/boards/${state.board.id}/columns/order`, { ids }));
    };
    const row = h('div', { class: 'col-row' },
      h('div', { class: 'row' },
        name,
        h('button', { class: 'btn sm', title: 'Move left', disabled: i === 0, onclick: () => move(-1) }, '◀'),
        h('button', { class: 'btn sm', title: 'Move right', disabled: i === state.columns.length - 1, onclick: () => move(1) }, '▶'),
        h('button', { class: 'btn sm danger', disabled: state.columns.length === 1, onclick: () => del() }, 'Delete')),
      h('div', { class: 'row' }, swatches, h('span', { class: 'muted xs' }, `${count} game${count === 1 ? '' : 's'}`)));

    // Empty columns go straight away; full ones ask where their games should move.
    const del = () => {
      if (!count) {
        if (confirm(`Delete the "${c.name}" column?`)) run(() => api('DELETE', `/columns/${c.id}`));
        return;
      }
      if (row.querySelector('.col-del')) return;
      const target = h('select', {}, state.columns.filter(x => x.id !== c.id).map(x => h('option', { value: x.id }, x.name)));
      row.append(h('div', { class: 'row col-del' },
        `Move its ${count} game${count === 1 ? '' : 's'} to`, target,
        h('button', { class: 'btn sm danger', onclick: () => run(() => api('DELETE', `/columns/${c.id}?moveTo=${target.value}`)) }, 'Delete column'),
        h('button', { class: 'btn sm ghost', onclick: () => draw() }, 'Cancel')));
    };
    return row;
  };

  const draw = () => {
    const counts = {};
    for (const g of state.games) counts[g.columnId] = (counts[g.columnId] || 0) + 1;
    const newName = h('input', { placeholder: 'New column, e.g. "Co-op night"', maxlength: 40, 'aria-label': 'New column name' });
    const addForm = h('form', {
      class: 'row',
      onsubmit: e => {
        e.preventDefault();
        if (newName.value.trim()) run(() => api('POST', `/boards/${state.board.id}/columns`, { name: newName.value }));
      },
    }, newName, h('button', { class: 'btn primary', type: 'submit' }, 'Add column'));
    root.replaceChildren(
      h('p', { class: 'muted xs' }, 'Columns belong to this board. Changes save as you make them.'),
      h('div', { class: 'col-editor' }, state.columns.map((c, i) => columnRow(c, i, counts[c.id] || 0))),
      addForm);
    if (focusNew) {
      newName.focus();
      focusNew = false;
    }
  };

  openModal('Edit Columns', root);
  draw();
}

function gameCard(g) {
  const m = g.meta || {};
  const img = safeUrl(m.headerUrl) || safeUrl(m.coverUrl);
  const card = h('article', { class: 'game-card', draggable: state.board.canEdit ? 'true' : null, dataset: { id: g.id }, onclick: () => openGame(g.id) },
    h('div', { class: 'card-img' },
      img ? h('img', { src: img, alt: '', loading: 'lazy' }) : h('span', { class: 'placeholder' }, g.title.slice(0, 1))),
    h('div', { class: 'card-body' },
      h('div', { class: 'card-title' }, g.title),
      h('div', { class: 'badges' }, priceBadge(m), protonBadge(m), modeBadge(m)),
      h('div', { class: 'card-foot' },
        interestDots(g),
        h('span', { class: 'note-count' },
          g.musicCount ? h('span', { title: `${g.musicCount} song(s) / playlist(s)` }, `♪ ${g.musicCount}`) : null,
          g.noteCount ? h('span', { title: `${g.noteCount} note(s)` }, `✎ ${g.noteCount}`) : null))));
  if (!state.board.canEdit) return card;
  card.addEventListener('dragstart', e => {
    state.dragging = true;
    e.dataTransfer.setData('text/plain', String(g.id));
    e.dataTransfer.effectAllowed = 'move';
    requestAnimationFrame(() => card.classList.add('dragging'));
  });
  card.addEventListener('dragend', () => {
    state.dragging = false;
    drawColumns();
  });
  return card;
}

function priceBadge(m) {
  const p = m.price;
  if (!p) return null;
  if (p.free) return h('span', { class: 'badge price' }, 'Free');
  if (!p.final) return null;
  if (p.discountPercent) return h('span', { class: 'badge price sale', title: `was ${p.initial}` }, `-${p.discountPercent}% ${p.final}`);
  return h('span', { class: 'badge price' }, p.final);
}

function protonBadge(m) {
  const t = m.proton && m.proton.tier;
  if (t && t !== 'pending') {
    return h('span', { class: `badge tier tier-${t}`, title: `ProtonDB: ${t} from ${m.proton.total} reports` }, cap(t));
  }
  if (m.nativeLinux) return h('span', { class: 'badge tier tier-native', title: 'Has a native Linux build' }, 'Linux');
  return null;
}

function modeBadge(m) {
  const modes = (m.modes || []).join(' ');
  if (/co-?op/i.test(modes)) return h('span', { class: 'badge mode' }, 'Co-op');
  if (/multi|pvp|mmo/i.test(modes)) return h('span', { class: 'badge mode' }, 'Multiplayer');
  return null;
}

function interestDots(g) {
  if (!g.interests.length) return h('span', { class: 'muted xs' }, 'No votes yet');
  return h('div', { class: 'dots' }, g.interests.map(i => {
    const l = levelInfo(i.level);
    return avatar(i.userId, i.username, `${l ? l.cls : 'none'}${i.owns ? ' owns' : ''}`, interestText(i));
  }));
}

// Drag-and-drop between and within columns. The dragged card is moved in the
// DOM while hovering; on drop its new position is the midpoint of its neighbours.
function attachDrop(body) {
  body.addEventListener('dragover', e => {
    const dragging = document.querySelector('.game-card.dragging');
    if (!dragging) return;
    e.preventDefault();
    body.classList.add('drop-target');
    const after = cardAfter(body, e.clientY);
    if (after) body.insertBefore(dragging, after);
    else body.appendChild(dragging);
  });
  body.addEventListener('dragleave', e => {
    if (!body.contains(e.relatedTarget)) body.classList.remove('drop-target');
  });
  body.addEventListener('drop', async e => {
    e.preventDefault();
    body.classList.remove('drop-target');
    const id = Number(e.dataTransfer.getData('text/plain'));
    const g = state.games.find(x => x.id === id);
    if (!g) return;
    const ids = [...body.querySelectorAll('.game-card')].map(el => Number(el.dataset.id));
    const idx = ids.indexOf(id);
    const prev = state.games.find(x => x.id === ids[idx - 1]);
    const next = state.games.find(x => x.id === ids[idx + 1]);
    let pos;
    if (prev && next) pos = (prev.position + next.position) / 2;
    else if (prev) pos = prev.position + 1;
    else if (next) pos = next.position - 1;
    else pos = 0;
    const columnId = Number(body.dataset.column);
    if (g.columnId === columnId && g.position === pos) return;
    g.columnId = columnId;
    g.position = pos;
    try {
      await api('PATCH', `/games/${id}`, { columnId, position: pos });
    } catch (ex) {
      toast(ex.message);
      if (await loadBoard(state.board.id)) drawColumns();
    }
  });
}

function cardAfter(container, y) {
  for (const el of container.querySelectorAll('.game-card:not(.dragging)')) {
    const box = el.getBoundingClientRect();
    if (y < box.top + box.height / 2) return el;
  }
  return null;
}

// openEditBoard edits a board's name and description and, for its owner (or
// an admin), who else may edit it.
async function openEditBoard() {
  let users = [];
  if (state.board.canManage) {
    try { users = await api('GET', '/users'); } catch (e) { return toast(e.message); }
  }
  const root = h('div', { class: 'stack' });

  // Member changes return the updated board; the header's editor list redraws too.
  const memberAction = async (fn, after) => {
    try {
      state.board = await fn();
      drawBoard();
      if (after) after();
      else draw();
    } catch (ex) { toast(ex.message); }
  };

  const draw = () => {
    const b = state.board;
    const name = h('input', { value: b.name, maxlength: 100, required: true });
    const desc = h('textarea', { rows: 3, maxlength: 1000, value: b.description });
    // Only the owner (or an admin) decides whether a board is personal.
    const personal = b.canManage ? h('input', { type: 'checkbox', checked: b.personal }) : null;
    const details = h('form', {
      class: 'stack',
      onsubmit: async e => {
        e.preventDefault();
        const body = { name: name.value, description: desc.value };
        if (personal) body.personal = personal.checked;
        try {
          state.board = await api('PATCH', `/boards/${b.id}`, body);
          closeModal();
          drawBoard();
        } catch (ex) { toast(ex.message); }
      },
    },
    h('label', { class: 'field' }, h('span', {}, 'Name'), name),
    h('label', { class: 'field' }, h('span', {}, 'Description'), desc),
    personal ? h('label', { class: 'check' }, personal, 'Personal board: nobody can request to join (clears any waiting requests)') : null,
    h('div', { class: 'row end' }, h('button', { class: 'btn primary', type: 'submit' }, 'Save')));

    const editorRows = [
      h('div', { class: 'admin-row' },
        h('strong', { class: 'grow' }, b.createdBy || 'deleted user'), h('span', { class: 'pill' }, 'owner')),
      ...b.members.map(m => {
        const isMe = m.id === state.user.id;
        let action = null;
        if (b.canManage) {
          action = h('button', { class: 'btn sm danger', onclick: () => memberAction(() => api('DELETE', `/boards/${b.id}/members/${m.id}`)) }, 'Remove');
        } else if (isMe) {
          action = h('button', {
            class: 'btn sm danger', onclick: () => {
              if (confirm('Stop being an editor of this board?')) {
                memberAction(() => api('DELETE', `/boards/${b.id}/members/${m.id}`), closeModal);
              }
            },
          }, 'Leave board');
        }
        return h('div', { class: 'admin-row' },
          h('strong', { class: 'grow' }, m.username, isMe ? h('span', { class: 'pill' }, 'you') : null),
          action);
      }),
    ];

    let addRow = null;
    if (b.canManage) {
      const taken = new Set([b.createdById, ...b.members.map(m => m.id)]);
      const options = users.filter(x => !taken.has(x.id));
      if (options.length) {
        const pick = h('select', { 'aria-label': 'Person to add' }, options.map(x => h('option', { value: x.id }, x.username)));
        addRow = h('div', { class: 'row' }, pick, h('button', {
          class: 'btn primary sm',
          onclick: () => memberAction(() => api('POST', `/boards/${b.id}/members`, { userId: Number(pick.value) })),
        }, 'Add editor'));
      } else {
        addRow = h('p', { class: 'muted xs' }, 'Everyone is already an editor.');
      }
    }

    const requestRows = b.canManage && b.requests.length ? [
      h('h3', {}, `Join requests (${b.requests.length})`),
      h('div', { class: 'admin-list' }, b.requests.map(r => h('div', { class: 'admin-row' },
        h('div', { class: 'grow' }, h('strong', {}, r.username), h('span', { class: 'muted xs' }, ` asked ${ago(r.createdAt)}`)),
        h('button', { class: 'btn sm primary', onclick: () => memberAction(() => api('POST', `/boards/${b.id}/requests/${r.userId}/approve`)) }, 'Approve'),
        h('button', { class: 'btn sm danger', onclick: () => memberAction(() => api('DELETE', `/boards/${b.id}/requests/${r.userId}`)) }, 'Decline')))),
    ] : [];

    const deleteRow = b.canManage ? h('div', { class: 'row between danger-zone' },
      h('span', { class: 'muted xs' }, 'Deleting removes every game and note on this board.'),
      h('button', {
        class: 'btn danger', type: 'button',
        onclick: async () => {
          if (!confirm(`Delete "${b.name}" and all ${state.games.length} games on it? This can't be undone.`)) return;
          try {
            await api('DELETE', `/boards/${b.id}`);
            closeModal();
            location.hash = '#/';
          } catch (ex) { toast(ex.message); }
        },
      }, 'Delete board')) : null;

    // replaceChildren would print a null as "null", so drop the rows this person doesn't get.
    root.replaceChildren(...[
      details,
      ...requestRows,
      h('h3', {}, 'Editors'),
      h('p', { class: 'muted xs' }, 'Everyone can see this board, vote and leave notes. Only these people can add, move or remove games and change columns.'),
      h('div', { class: 'admin-list' }, editorRows),
      addRow,
      deleteRow,
    ].filter(Boolean));
  };

  openModal('Board Settings', root);
  draw();
}

// ---------- add game ----------

function openAddGame() {
  const input = h('input', { type: 'search', placeholder: 'Search Steam & IGDB, or paste a Steam / SteamDB / IGDB link…', 'aria-label': 'Search games' });
  const results = h('div', { class: 'search-results' });
  let timer, seq = 0;

  input.addEventListener('input', () => {
    clearTimeout(timer);
    timer = setTimeout(run, 350);
  });

  async function run() {
    const q = input.value.trim();
    const my = ++seq;
    if (q.length < 2) { results.replaceChildren(); return; }
    results.replaceChildren(h('div', { class: 'muted pad' }, 'Searching…'));
    let d;
    try {
      d = await api('GET', '/search?q=' + encodeURIComponent(q));
    } catch (e) {
      if (my === seq) results.replaceChildren(h('div', { class: 'form-error' }, e.message));
      return;
    }
    if (my !== seq) return;
    const groups = [];
    if (d.steam.length) groups.push(h('h4', {}, 'Steam'), d.steam.map(r => resultRow(r, 'steam')));
    if (d.igdb.length) groups.push(h('h4', {}, 'IGDB'), d.igdb.map(r => resultRow(r, 'igdb')));
    if (!groups.length) groups.push(h('div', { class: 'muted pad' }, 'No matches.'));
    if (d.errors && d.errors.length) groups.push(h('div', { class: 'form-error' }, d.errors.join(', ')));
    if (!d.igdbEnabled) groups.push(h('p', { class: 'muted xs pad' }, 'IGDB search is off: set TWITCH_CLIENT_ID and TWITCH_CLIENT_SECRET on the server to enable it.'));
    groups.push(h('div', { class: 'manual-add' },
      h('button', { class: 'btn sm ghost', onclick: e => add({ title: q }, e.currentTarget) }, `Add "${q}" without metadata`)));
    results.replaceChildren(...groups.flat());
  }

  function resultRow(item, source) {
    const onBoard = state.games.some(g =>
      (item.steamAppId && g.steamAppId === item.steamAppId) || (item.igdbId && g.igdbId === item.igdbId));
    const btn = h('button', { class: 'btn sm primary', disabled: onBoard, onclick: () => add(item, btn) }, onBoard ? 'On board' : 'Add');
    const img = safeUrl(item.image);
    return h('div', { class: 'result' },
      img ? h('img', { src: img, alt: '', class: source === 'igdb' ? 'cover' : 'capsule', loading: 'lazy' }) : h('div', { class: 'result-ph' }),
      h('div', { class: 'result-text' },
        h('div', { class: 'result-title' }, item.title),
        h('div', { class: 'muted xs' }, [item.year, item.price, item.platforms && item.platforms.join(', ')].filter(Boolean).join(' · '))),
      btn);
  }

  async function add(item, btn) {
    btn.disabled = true;
    const label = btn.textContent;
    btn.textContent = 'Adding…';
    try {
      const g = await api('POST', `/boards/${state.board.id}/games`, { title: item.title, steamAppId: item.steamAppId, igdbId: item.igdbId });
      state.games.push(g);
      drawColumns();
      btn.textContent = 'Added ✓';
      toast(`Added ${g.title}`, 'ok');
    } catch (e) {
      btn.disabled = false;
      btn.textContent = label;
      toast(e.message);
    }
  }

  openModal('Add a Game', h('div', { class: 'stack' },
    h('p', { class: 'muted xs' }, `New games land in "${state.columns.length ? state.columns[0].name : 'the first column'}". Metadata comes from Steam, ProtonDB and IGDB.`),
    input,
    results), true);
  input.focus();
}

// ---------- game detail ----------

async function openGame(id) {
  let data;
  try {
    [data] = await Promise.all([
      api('GET', `/games/${id}`),
      api('GET', '/boards').then(b => { state.boards = b; }),
    ]);
  } catch (e) { return toast(e.message); }
  state.noteDraft = '';
  state.manageOpen = false;
  const root = h('div', { class: 'game-detail' });
  openModal('Game Info', root, true);
  drawGame(root, data);
}

// syncGame copies a fresh game from the server into the board state.
function syncGame(g) {
  const i = state.games.findIndex(x => x.id === g.id);
  if (!state.board || g.boardId !== state.board.id) {
    if (i >= 0) state.games.splice(i, 1);
  } else if (i >= 0) state.games[i] = g;
  else state.games.push(g);
  drawColumns();
}

function priceText(m) {
  const p = m.price;
  if (!p) return null;
  if (p.free) return 'Free';
  if (p.discountPercent) return `${p.final} (-${p.discountPercent}%, was ${p.initial})`;
  return p.final;
}

function protonText(m) {
  const p = m.proton;
  if (!p || !p.tier) return m.nativeLinux ? 'Native Linux build' : null;
  let s = `${cap(p.tier)} · ${p.total} reports`;
  if (p.trendingTier && p.trendingTier !== p.tier) s += ` · trending ${p.trendingTier}`;
  if (m.nativeLinux) s += ' · native Linux';
  return s;
}

function drawGame(root, data) {
  const { game: g, notes } = data;
  const music = data.music || [];
  syncGame(g);
  const m = g.meta || {};
  const links = m.links || {};
  const me = g.interests.find(i => i.userId === state.user.id) || { level: 0, owns: false };

  const act = async fn => {
    try {
      const d = await fn();
      if (d && d.game) drawGame(root, d);
    } catch (e) { toast(e.message); }
  };
  const setInterest = (level, owns) => act(() => api('PUT', `/games/${g.id}/interest`, { level, owns }));

  const img = safeUrl(m.headerUrl) || safeUrl(m.coverUrl);
  const linkBtns = [
    ['Steam', links.steam], ['SteamDB', links.steamdb], ['ProtonDB', links.protondb], ['IGDB', links.igdb], ['Website', m.website],
  ].filter(([, u]) => safeUrl(u))
    .map(([label, u]) => h('a', { class: 'btn sm', href: u, target: '_blank', rel: 'noopener noreferrer' }, label + ' ↗'));

  const facts = [
    ['Release', m.releaseDate && (m.releaseDate + (m.comingSoon ? ' (upcoming)' : ''))],
    ['Developer', m.developers && m.developers.join(', ')],
    ['Publisher', m.publishers && m.publishers.join(', ')],
    ['Genres', m.genres && m.genres.join(', ')],
    ['Modes', m.modes && m.modes.join(', ')],
    ['Platforms', m.platforms && m.platforms.join(', ')],
    ['Price', priceText(m)],
    ['ProtonDB', protonText(m)],
    ['Metacritic', m.metacritic],
    ['IGDB rating', m.igdbRating && `${m.igdbRating}/100`],
  ].filter(([, v]) => v);

  const editable = state.board.canEdit;
  const columnSel = h('select', {
    disabled: !editable,
    title: editable ? null : 'Only editors can move games',
    onchange: e => act(() => api('PATCH', `/games/${g.id}`, { columnId: Number(e.target.value) })),
  }, state.columns.map(c => h('option', { value: c.id }, c.name)));
  columnSel.value = g.columnId;

  const levelBtns = h('div', { class: 'seg' }, LEVELS.map(l => h('button', {
    class: `seg-btn ${l.cls}${me.level === l.level ? ' active' : ''}`,
    onclick: () => setInterest(me.level === l.level ? 0 : l.level, me.owns),
  }, l.label)));
  const ownsBox = h('label', { class: 'check' },
    h('input', { type: 'checkbox', checked: me.owns, onchange: e => setInterest(me.level, e.target.checked) }), 'I own it');

  const owners = g.interests.filter(i => i.owns).length;
  const group = g.interests.length
    ? h('ul', { class: 'group' }, g.interests.map(i => {
      const l = levelInfo(i.level);
      return h('li', {},
        avatar(i.userId, i.username, `${l ? l.cls : 'none'}${i.owns ? ' owns' : ''}`),
        h('span', { class: 'grow' }, i.username),
        h('span', { class: 'muted xs' }, [l && l.label, i.owns && 'owns it'].filter(Boolean).join(' · ')));
    }))
    : h('p', { class: 'muted xs' }, 'Nobody has voted yet.');

  // Notes
  const noteInput = h('textarea', {
    rows: 3, maxlength: 10000, placeholder: 'Add a note: price to wait for, who has it, mods to try…', value: state.noteDraft,
    oninput: e => { state.noteDraft = e.target.value; },
    onkeydown: e => { if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') noteForm.requestSubmit(); },
  });
  const noteForm = h('form', {
    class: 'note-form',
    onsubmit: e => {
      e.preventDefault();
      const body = noteInput.value.trim();
      if (!body) return;
      act(async () => {
        const d = await api('POST', `/games/${g.id}/notes`, { body });
        state.noteDraft = '';
        return d;
      });
    },
  }, noteInput, h('div', { class: 'row end' }, h('span', { class: 'muted xs' }, 'Ctrl+Enter to post'), h('button', { class: 'btn primary sm', type: 'submit' }, 'Post note')));

  // Manage section: fix links, refresh, rename, move, delete
  const steamIn = h('input', { type: 'number', min: 1, placeholder: 'e.g. 1145360', value: g.steamAppId ?? '' });
  const igdbIn = h('input', { type: 'number', min: 1, placeholder: 'IGDB game id', value: g.igdbId ?? '' });
  const titleIn = h('input', { value: g.title, maxlength: 200 });
  const boardSel = h('select', {}, (state.boards || []).filter(b => b.canEdit).map(b => h('option', { value: b.id }, b.name)));
  boardSel.value = g.boardId;
  const numOrNull = v => (v.trim() ? Number(v) : null);

  const manage = h('details', { class: 'manage', ontoggle: e => { state.manageOpen = e.target.open; } },
    h('summary', {}, 'Manage game'),
    h('div', { class: 'stack' },
      h('label', { class: 'field' }, h('span', {}, 'Title'),
        h('div', { class: 'row' }, titleIn, h('button', { class: 'btn sm', onclick: () => act(() => api('PATCH', `/games/${g.id}`, { title: titleIn.value })) }, 'Rename'))),
      h('label', { class: 'field' }, h('span', {}, 'Steam app id'), steamIn),
      h('label', { class: 'field' }, h('span', {}, 'IGDB id'), igdbIn),
      h('div', { class: 'row' },
        h('button', {
          class: 'btn sm', onclick: e => {
            e.currentTarget.textContent = 'Saving…';
            act(() => api('PUT', `/games/${g.id}/links`, { steamAppId: numOrNull(steamIn.value), igdbId: numOrNull(igdbIn.value) }));
          },
        }, 'Save ids & refetch'),
        h('button', {
          class: 'btn sm ghost', onclick: e => {
            e.currentTarget.textContent = 'Refreshing…';
            act(() => api('POST', `/games/${g.id}/refresh`));
          },
        }, 'Refresh metadata')),
      h('p', { class: 'muted xs' }, g.metaUpdatedAt ? `Metadata fetched ${ago(g.metaUpdatedAt)}.` : 'No metadata fetched yet.'),
      h('label', { class: 'field' }, h('span', {}, 'Move to board'),
        h('div', { class: 'row' }, boardSel, h('button', {
          class: 'btn sm', onclick: async () => {
            const to = Number(boardSel.value);
            if (to === g.boardId) return;
            try {
              const d = await api('PATCH', `/games/${g.id}`, { boardId: to });
              syncGame(d.game);
              closeModal();
              toast(`Moved ${g.title}`, 'ok');
            } catch (ex) { toast(ex.message); }
          },
        }, 'Move'))),
      h('button', {
        class: 'btn danger sm', onclick: async () => {
          if (!confirm(`Remove ${g.title} and its notes from this board?`)) return;
          try {
            await api('DELETE', `/games/${g.id}`);
            state.games = state.games.filter(x => x.id !== g.id);
            drawColumns();
            closeModal();
          } catch (ex) { toast(ex.message); }
        },
      }, 'Delete game')));
  manage.open = state.manageOpen;

  root.replaceChildren(...[
    h('div', { class: 'hero' + (img ? '' : ' no-img') },
      img ? h('img', { src: img, alt: '' }) : null,
      h('div', { class: 'hero-text' },
        h('h2', {}, g.title),
        h('div', { class: 'muted xs' }, `Added by ${g.addedBy || 'someone'} ${ago(g.createdAt)}`))),
    linkBtns.length ? h('div', { class: 'link-row' }, linkBtns) : null,
    h('div', { class: 'detail-grid' },
      h('div', { class: 'detail-main' },
        m.summary ? h('p', { class: 'summary' }, m.summary)
          : h('p', { class: 'muted' }, editable ? 'No description. Link a Steam or IGDB entry under "Manage game".' : 'No description.'),
        facts.length ? h('dl', { class: 'facts' }, facts.map(([k, v]) => [h('dt', {}, k), h('dd', {}, String(v))])) : null,
        h('h3', {}, `Notes (${notes.length})`),
        noteForm,
        h('div', { class: 'notes' }, notes.map(n => noteItem(n, g, act)))),
      h('aside', { class: 'detail-side' },
        h('label', { class: 'field' }, h('span', {}, 'Column'), columnSel),
        h('div', { class: 'field' }, h('span', {}, 'Your take'), levelBtns, ownsBox),
        h('div', { class: 'field' }, h('span', {}, `The group${owners ? ` · ${owners} own it` : ''}`), group),
        gameMusicSection(g, music, data.playlists || [], editable, act),
        editable ? manage : null)),
  ].filter(Boolean));
}

// gameMusicSection is a game's soundtrack in its details window. Anyone can
// play it; the board's editors can add and remove songs and playlists.
function gameMusicSection(g, music, playlists, editable, act) {
  let adding = null;
  const total = music.length + playlists.length;
  // Play all: the game's own songs, then each playlist's, each song once.
  const playEverything = async () => {
    try {
      const lists = await Promise.all(playlists.map(pl => api('GET', `/playlists/${pl.id}`)));
      const seen = new Set();
      const all = music.concat(...lists.map(l => l.items)).filter(m => !seen.has(m.id) && seen.add(m.id));
      if (!all.length) return toast('Nothing to play yet');
      playAll(all);
    } catch (e) { toast(e.message); }
  };
  const addBtn = editable ? h('button', {
    class: 'link xs', type: 'button',
    onclick: () => {
      if (adding) {
        adding.remove();
        adding = null;
        addBtn.textContent = '+ Add music';
        return;
      }
      adding = musicLinkBox(
        m => act(() => api('POST', `/games/${g.id}/music`, { musicId: m.id })),
        null,
        pl => act(() => api('POST', `/games/${g.id}/playlists`, { playlistId: pl.id })));
      addBtn.textContent = 'Cancel';
      addBtn.after(adding);
    },
  }, '+ Add music') : null;
  return h('div', { class: 'field' },
    h('div', { class: 'row between' },
      h('span', {}, `Music${total ? ` (${total})` : ''}`),
      total ? h('button', { class: 'btn sm', type: 'button', onclick: playEverything }, '▶ Play all') : null),
    total
      ? h('div', { class: 'stack music-list' },
        playlists.map(pl => playlistChip(pl, editable
          ? () => act(() => api('DELETE', `/games/${g.id}/playlists/${pl.id}`))
          : null)),
        music.map(m => musicChip(m, editable
          ? () => act(() => api('DELETE', `/games/${g.id}/music/${m.id}`))
          : null)))
      : h('p', { class: 'muted xs' }, editable ? 'Give this game a soundtrack: paste a Spotify or YouTube Music link, or pick a playlist.' : 'No music yet.'),
    addBtn);
}

function noteItem(n, g, act) {
  const isAuthor = n.userId === state.user.id;
  const canDelete = isAuthor || (state.user.isAdmin && state.user.adminMode);
  const body = h('div', { class: 'note-body' }, linkify(n.body));

  const edit = () => {
    const ta = h('textarea', { rows: 4, maxlength: 10000, value: n.body });
    body.replaceWith(h('div', { class: 'stack' }, ta, h('div', { class: 'row end' },
      h('button', { class: 'btn sm ghost', onclick: () => act(() => api('GET', `/games/${g.id}`)) }, 'Cancel'),
      h('button', { class: 'btn sm primary', onclick: () => act(() => api('PATCH', `/notes/${n.id}`, { body: ta.value })) }, 'Save'))));
    ta.focus();
  };
  const del = () => {
    if (confirm('Delete this note?')) act(() => api('DELETE', `/notes/${n.id}`));
  };

  return h('div', { class: 'note' },
    h('div', { class: 'note-head' },
      n.username ? avatar(n.userId, n.username, 'none') : null,
      h('strong', {}, n.username || 'deleted user'),
      h('span', { class: 'muted xs' }, ago(n.createdAt) + (n.updatedAt > n.createdAt + 5 ? ' (edited)' : '')),
      h('span', { class: 'spacer' }),
      isAuthor ? h('button', { class: 'link xs', onclick: edit }, 'Edit') : null,
      canDelete ? h('button', { class: 'link xs danger', onclick: del }, 'Delete') : null),
    body);
}

route();
