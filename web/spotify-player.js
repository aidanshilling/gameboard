// Spotify's embed controller needs eval, which the main page's security
// policy doesn't allow. So it runs here instead, in a page the music player
// frames, and this relays its events and the player's commands with
// postMessage, only ever to and from the page that framed it.

const uri = new URLSearchParams(location.hash.slice(1)).get('uri') || '';
const send = (type, data) => parent.postMessage({ spotify: type, data }, location.origin);

if (parent !== window && /^spotify:(track|album|playlist|episode|show|artist):[A-Za-z0-9]{22}$/.test(uri)) {
  document.documentElement.style.background = 'transparent';
  document.body.style.margin = '0';
  const target = document.createElement('div');
  document.body.append(target);

  let controller = null;
  window.addEventListener('message', e => {
    if (e.source !== parent || e.origin !== location.origin || !controller || !e.data) return;
    if (e.data.cmd === 'toggle') controller.togglePlay();
    else if (e.data.cmd === 'play') controller.play();
  });

  window.onSpotifyIframeApiReady = api => {
    api.createController(target, { uri, width: '100%', height: 152 }, c => {
      controller = c;
      c.addListener('ready', () => send('ready'));
      c.addListener('playback_update', e => {
        const { isPaused, position, duration } = e.data;
        send('update', { isPaused, position, duration });
      });
    });
  };
  const s = document.createElement('script');
  s.src = 'https://open.spotify.com/embed/iframe-api/v1';
  s.onerror = () => send('error');
  document.head.append(s);
}
