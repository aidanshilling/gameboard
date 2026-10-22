# pingugamer

A small self-hosted app for a group of friends to plan which games to buy and play together.

- Boards, laid out as columns each board names for itself. Drag cards between columns. Everyone can see every board; only its owner and the editors they add can change it. Others can ask to join, unless it's a personal board.
- Search Steam and IGDB, or paste a Steam / SteamDB / ProtonDB / IGDB link, to add a game.
- Metadata is pulled automatically:
  - From Steam: price and sales, co-op/multiplayer modes, genres, release date and Metacritic score.
  - From ProtonDB: the compatibility tier.
  - From IGDB: summary, cover and rating.

  Each game links out to Steam, SteamDB, ProtonDB and IGDB.
- Each person can vote on a game (Want to play / Maybe / Pass), mark whether they own it, and leave notes.
- Top Games, at the top of the boards page, has three tabs:
  - **Most wanted:** the games with the most "Want to play" votes, with the faces of who wants them.
  - **Most owned:** the games the most people own.
  - **On sale for you:** discounted games that are on your boards, or that you want to play, and that you don't own yet.

  The same game on several boards counts once. Steam prices are re-checked in the background, 100 games at a time every half hour, for any price over 6 hours old.
- A site-wide chat, plus one per board. Posts can link a game or a song.
- Music: paste Spotify or YouTube (Music) links into chat, or give a game a soundtrack. Everything plays in a small floating player with a queue and a volume slider (YouTube only). The Music page lists everything shared and has playlists: mix songs from both services, then link a playlist in chat or put it on a game. Only a playlist's owner can change it. Spotify plays 30-second previews unless you're logged in to Spotify in the same browser; the player shows a "Log in to Spotify" button when that happens. Some browsers, like Safari, keep that login away from players on other sites.
- A weekly ticker along the bottom, profile pictures and themes.

It runs as one Go binary with the frontend embedded, plus one SQLite file. No CGO and no Node build step.

## Run it

```sh
go build -o gameboard .
./gameboard create-admin yourname   # prints a generated password
./gameboard
# open http://localhost:8080, log in, change the password
```

### Accounts

- **Admins** can only be created on the server, with `gameboard create-admin`. Signing up through the website never makes anyone an admin.
- **Sign-ups** go into a separate `pending_users` table. A pending request has no user row and no session, so it grants no access. Trying to log in with it says "waiting for approval".
- **The Admin page** (top bar, admins only) lists pending requests to approve or reject. Approving creates a real user with the username and password the person chose. From the same page you can also create accounts directly, reset passwords and remove users.
- **Approved users** get full access: they can create and edit boards, games, votes and notes.
- **Admin accounts can't be removed or reset from the web page.** Use the CLI (`remove-admin`, `reset-password`).
- **Spam limits:** sign-up requests are limited to 5 per IP per hour, and at most 25 can wait at once. Set `ALLOW_SIGNUPS=false` to turn sign-ups off completely, so accounts can only be created by admins.

Cross-compile for a Linux server from any OS:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o gameboard .
```

Or use Docker:

```sh
docker build -t gameboard .
docker run -d --name gameboard -p 8080:8080 -v gameboard-data:/data gameboard
docker exec gameboard gameboard create-admin yourname
```

## Production: games.pingulon.net (Google Cloud)

The site runs on a free-tier `e2-micro` VM named `gameboard`, in project `pingulondotnet`, zone `us-east1-b`, at static IP `35.196.3.25`. The VM runs Debian, with Caddy in front of the app for automatic HTTPS. DNS is an A record in Cloudflare, set to DNS only.

Deploy a new version from Windows (needs Go and gcloud):

```powershell
.\deploy\deploy.ps1
```

This cross-compiles the app, uploads it with the files in `deploy/`, and runs `setup-server.sh` on the VM. The script is safe to re-run.

On the server (`gcloud compute ssh gameboard`):

- Admin commands: `gameboard-admin list-users`, `gameboard-admin create-admin <name>`, …
- Logs: `journalctl -u gameboard -f` and `journalctl -u caddy`
- Secrets and settings, such as the IGDB keys: put `KEY=value` lines in `/etc/gameboard/env`, then run `sudo systemctl restart gameboard`
- To check the IGDB keys work, copy `deploy/igdb-check.sh` to the server and run `sudo sh igdb-check.sh`. It runs the app's IGDB queries without printing the keys.
- Database: `/var/lib/gameboard/gameboard.db`. It's backed up nightly to `/var/lib/gameboard/backups/`, and backups are kept for 14 days.

## Configuration (env vars)

| Variable | Default | Purpose |
|---|---|---|
| `ADDR` | `:8080` | Listen address |
| `DB_PATH` | `gameboard.db` | SQLite file path |
| `ALLOW_SIGNUPS` | `true` | `false` disables account requests entirely (admins create accounts) |
| `TWITCH_CLIENT_ID` / `TWITCH_CLIENT_SECRET` | *(empty)* | Turns on IGDB (see below) |
| `STEAM_COUNTRY` | `US` | Country code for Steam prices (`GB`, `DE`, `CA`, …) |
| `COOKIE_SECURE` | `auto` | `true` when served over HTTPS. `auto` detects TLS, or `X-Forwarded-Proto` when `TRUST_PROXY=true`. |
| `TRUST_PROXY` | `false` | Set to `true` behind a reverse proxy (Caddy, nginx) so client IPs and HTTPS are detected correctly |

### Enabling IGDB

IGDB is free but uses Twitch app credentials:

1. Log in at <https://dev.twitch.tv/console/apps> (Twitch 2FA must be enabled).
2. Register an application. Use `http://localhost` as the OAuth redirect URL and pick the **Confidential** client type.
3. Copy the Client ID, generate a Client Secret, and set them as `TWITCH_CLIENT_ID` and `TWITCH_CLIENT_SECRET`.

Without these, everything still works using Steam and ProtonDB data only.

## HTTPS

Put the app behind a reverse proxy. With [Caddy](https://caddyserver.com/), a `Caddyfile` like this gets you automatic certificates:

```
games.example.com {
    reverse_proxy localhost:8080
}
```

Then run the app with `TRUST_PROXY=true ADDR=127.0.0.1:8080`.

## Admin commands

```sh
./gameboard create-admin <username>     # new admin account, prints its password
./gameboard make-admin <username>       # promote an existing user
./gameboard remove-admin <username>     # demote back to a regular user
./gameboard reset-password <username>   # prints a new random password and logs out that user's sessions
./gameboard list-users                  # users and pending sign-ups
```

Use the same `DB_PATH` as the server. In Docker, run `docker exec gameboard gameboard <command>`.

## Backups

All data lives in the SQLite file. To back it up while the app is running:

```sh
sqlite3 gameboard.db ".backup backup.db"
```

## Limits

- **Boards:** up to 30 owned per person.
- **Games:** up to 300 per board.
- **Notes:** up to 200 per game.
- **Music:** up to 20 songs and 10 playlists per game. Each person can look up 30 new links per 5 minutes.
- **Playlists:** up to 50 per person, 200 songs each.

Disk isn't the constraint; a game with its metadata is about 1.5 KB. These caps keep boards quick to load and cheap to poll. They live at the top of `handlers.go`.

Moving between pages draws straight away from the data the page had last time, then checks with the server and redraws only if something changed. The menu, chat and footer stay in place. Pages that refresh themselves (boards every 15 seconds, chat every 20) send back a fingerprint of what they already have. The server replies `304 Not Modified` with no body unless something actually changed, so idle tabs use almost no bandwidth.

## Security notes

The app aims for sensible security, not high assurance:

- Passwords are hashed with bcrypt (cost 12). Login is rate-limited per IP and per username: 10 failures locks that key out for 15 minutes.
- Sessions use random 256-bit tokens. Only their SHA-256 hash is stored in the database. They live in an `HttpOnly`, `SameSite=Lax` cookie with a 30-day sliding expiry. Changing your password logs out your other sessions.
- CSRF protection: write requests must carry a custom header and a same-origin `Origin`.
- A strict Content Security Policy is set. No inline scripts or eval are allowed, and the frontend never renders user text as HTML. The only outside scripts allowed are YouTube's player API, and it loads only when someone presses play.
- Spotify's embed controller needs eval. So it runs in `web/spotify-player.html`, a separate page that holds no user content and only this site may frame. That page talks to the app with origin-checked `postMessage`.
- Sign-up requests are kept apart from real accounts until an admin approves them. Admin rights can only be granted from the server CLI.
- Only a board's owner and editors can change it. Admins can view everything; they get edit powers only after switching on admin edit mode.

## Layout

```
main.go       config, routes, middleware, CLI commands
auth.go       sign-up requests, login, sessions, rate limiting
admin.go      admin API: approve sign-ups, create/remove users, reset passwords
handlers.go   boards / games / notes / votes / search API
metadata.go   Steam, ProtonDB and IGDB clients
members.go    board editors and join requests
columns.go    per-board columns
feed.go       site and board chat
music.go      Spotify / YouTube links, game soundtracks, the Music page
playlists.go  people's own playlists
highlights.go Top Games (most wanted, most owned, on sale for you)
prices.go     background Steam price checks
ticker.go     the weekly ticker
avatar.go     profile pictures (resized and compressed on upload)
themes.go     preset and shared themes
filter.go     the chat's slur filter
db.go         SQLite schema
web/          frontend (embedded into the binary at build time)
```
