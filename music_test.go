package main

import (
	"errors"
	"testing"
)

func TestParseMusicURL(t *testing.T) {
	ok := []struct{ in, provider, kind, id string }{
		{"https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT", "spotify", "track", "4cOdK2wGLETKBW3PvgPWqT"},
		{"https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT?si=abc123", "spotify", "track", "4cOdK2wGLETKBW3PvgPWqT"},
		{"https://open.spotify.com/intl-de/album/1DFixLWuPkv3KT3TnV35m3", "spotify", "album", "1DFixLWuPkv3KT3TnV35m3"},
		{"https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M", "spotify", "playlist", "37i9dQZF1DXcBWIGoYBM5M"},
		{"spotify:track:4cOdK2wGLETKBW3PvgPWqT", "spotify", "track", "4cOdK2wGLETKBW3PvgPWqT"},
		{"  https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42  ", "youtube", "video", "dQw4w9WgXcQ"},
		{"https://music.youtube.com/watch?v=dQw4w9WgXcQ&list=RDAMVMdQw4w9WgXcQ", "youtube", "video", "dQw4w9WgXcQ"},
		{"https://youtu.be/dQw4w9WgXcQ?si=x", "youtube", "video", "dQw4w9WgXcQ"},
		{"https://m.youtube.com/watch?v=dQw4w9WgXcQ", "youtube", "video", "dQw4w9WgXcQ"},
		{"https://www.youtube.com/shorts/dQw4w9WgXcQ", "youtube", "video", "dQw4w9WgXcQ"},
		{"https://music.youtube.com/playlist?list=OLAK5uy_kB2Vb1jzL1XqfK1y3yp5J0D3YQ1t6s0Ro", "youtube", "playlist", "OLAK5uy_kB2Vb1jzL1XqfK1y3yp5J0D3YQ1t6s0Ro"},
	}
	for _, c := range ok {
		p, k, id, err := parseMusicURL(c.in)
		if err != nil || p != c.provider || k != c.kind || id != c.id {
			t.Errorf("parseMusicURL(%q) = %s %s %s %v, want %s %s %s", c.in, p, k, id, err, c.provider, c.kind, c.id)
		}
	}

	bad := []struct {
		in   string
		want error
	}{
		{"", errMusicLink},
		{"not a link", errMusicLink},
		{"https://evil.example/track/4cOdK2wGLETKBW3PvgPWqT", errMusicLink},
		{"https://open.spotify.com.evil.example/track/4cOdK2wGLETKBW3PvgPWqT", errMusicLink},
		{"https://open.spotify.com/track/short", errMusicLink},
		{"https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqTextra", errMusicLink},
		{"https://www.youtube.com/watch?v=dQw4\"onload=x", errMusicLink},
		{"https://www.youtube.com/watch?v=<script>", errMusicLink},
		{"javascript:alert(1)//youtube.com/watch?v=dQw4w9WgXcQ", errMusicLink},
		{"https://youtube.com.evil.example/watch?v=dQw4w9WgXcQ", errMusicLink},
		{"https://spotify.link/abc123", errMusicShort},
		{"https://music.youtube.com/browse/MPREb_abcdef", errMusicBrowse},
	}
	for _, c := range bad {
		if _, _, _, err := parseMusicURL(c.in); !errors.Is(err, c.want) {
			t.Errorf("parseMusicURL(%q) error = %v, want %v", c.in, err, c.want)
		}
	}
}
