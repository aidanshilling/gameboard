#!/bin/sh
# Checks the IGDB credentials in /etc/gameboard/env by running the same
# queries the app makes. Run on the server: sudo sh igdb-check.sh
set -eu
. /etc/gameboard/env

token=$(curl -fsS -X POST "https://id.twitch.tv/oauth2/token" \
	-d "client_id=$TWITCH_CLIENT_ID" -d "client_secret=$TWITCH_CLIENT_SECRET" -d "grant_type=client_credentials" |
	sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
[ -n "$token" ] || { echo "FAIL: could not get a Twitch token (check the client id/secret)"; exit 1; }
echo "token: ok"

igdb() {
	curl -sS -X POST "https://api.igdb.com/v4/$1" -H "Client-ID: $TWITCH_CLIENT_ID" \
		-H "Authorization: Bearer $token" -H "Content-Type: text/plain" -d "$2" -w '\nHTTP %{http_code}\n'
}

names() { grep -o '"name": "[^"]*"' | grep -v 'PC (Microsoft\|PlayStation\|Xbox\|Nintendo\|Linux\|Mac\|Android\|iOS' ; }
echo "--- search, no type filter"
igdb games 'search "deep rock"; fields name; where version_parent = null; limit 6;' | names
echo "--- search, filtered like the app (no DLC/seasons)"
igdb games 'search "deep rock"; fields name; where version_parent = null & game_type = (0,4,8,9,10,11); limit 6;' | names
echo "--- search, same filter, another game"
igdb games 'search "valheim"; fields name; where version_parent = null & game_type = (0,4,8,9,10,11); limit 6;' | names
echo "--- full game query used for metadata"
igdb games 'fields name,summary,url,first_release_date,total_rating,cover.image_id,genres.name,game_modes.name,
	platforms.name,platforms.abbreviation,involved_companies.company.name,involved_companies.developer,
	involved_companies.publisher,websites.url,external_games.uid,external_games.url; where id = 27134;' | grep -E 'HTTP|"url": "https://store.steampowered|"name": "(Co-operative|Multiplayer|Ghost Ship|Shooter)' | sort -u
