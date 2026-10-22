package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIMDbIDFrom(t *testing.T) {
	for in, want := range map[string]string{
		"tt0816692":                             "tt0816692",
		" tt0816692 ":                           "tt0816692",
		"https://www.imdb.com/title/tt0816692/": "tt0816692",
		"https://m.imdb.com/title/tt0816692/?ref_=x": "tt0816692",
		"https://www.imdb.com/de/title/tt12345678":   "tt12345678",
		"https://evil.example/title/tt0816692/":      "",
		"interstellar":                               "",
		"tt12":                                       "",
	} {
		if got := imdbIDFrom(in); got != want {
			t.Errorf("imdbIDFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

// A stand-in for OMDb.
func fakeOMDb(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("apikey") != "secret-key" {
			w.Write([]byte(`{"Response":"False","Error":"Invalid API key!"}`))
			return
		}
		switch {
		case q.Get("s") == "interstellar":
			w.Write([]byte(`{"Response":"True","Search":[
				{"Title":"Interstellar","Year":"2014","imdbID":"tt0816692","Type":"movie","Poster":"https://m.media-amazon.com/x.jpg"},
				{"Title":"Interstellar: game","Year":"2015","imdbID":"tt9999999","Type":"game","Poster":"N/A"},
				{"Title":"Bad id","Year":"2015","imdbID":"nope","Type":"movie","Poster":"N/A"}]}`))
		case q.Get("s") != "":
			w.Write([]byte(`{"Response":"False","Error":"Movie not found!"}`))
		case q.Get("i") == "tt0816692":
			w.Write([]byte(`{"Response":"True","Title":"Interstellar","Year":"2014","Rated":"PG-13","Runtime":"169 min",
				"Genre":"Adventure, Drama, Sci-Fi","Director":"Christopher Nolan","Actors":"Matthew McConaughey","Plot":"Space.",
				"Poster":"http://insecure.example/x.jpg","Metascore":"N/A","imdbRating":"8.7","imdbID":"tt0816692","Type":"movie",
				"Ratings":[{"Source":"Rotten Tomatoes","Value":"73%"}]}`))
		default:
			w.Write([]byte(`{"Response":"False","Error":"Incorrect IMDb ID."}`))
		}
	}))
}

func TestOMDb(t *testing.T) {
	srv := fakeOMDb(t)
	defer srv.Close()
	m := &metaClient{http: &http.Client{Timeout: 5 * time.Second}, omdbKey: "secret-key", omdbBase: srv.URL + "/"}
	ctx := context.Background()

	hits, err := m.movieSearch(ctx, "interstellar")
	if err != nil || len(hits) != 1 || hits[0].IMDbID != "tt0816692" || hits[0].Poster == "" {
		t.Fatalf("search = %+v, %v (games and bad ids should be dropped)", hits, err)
	}
	if hits, err := m.movieSearch(ctx, "zzzz"); err != nil || len(hits) != 0 {
		t.Errorf("not found = %+v, %v; want no hits and no error", hits, err)
	}
	mv, err := m.movieDetails(ctx, "tt0816692")
	if err != nil {
		t.Fatal(err)
	}
	if mv.Runtime != "169 min" || mv.RottenTomatoes != "73%" || mv.Metascore != "" || mv.Poster != "" {
		t.Errorf("details = %+v (N/A should be empty, http posters dropped)", mv)
	}
	if _, err := m.movieDetails(ctx, "tt0000001"); err != errNotFound {
		t.Errorf("unknown id: %v, want errNotFound", err)
	}

	// No key: switched off.
	off := &metaClient{http: http.DefaultClient}
	if _, err := off.movieSearch(ctx, "interstellar"); err != errMoviesOff {
		t.Errorf("without a key: %v", err)
	}

	// The key never shows in errors, even ones that quote the whole URL.
	srv.Close()
	_, err = m.movieSearch(ctx, "interstellar")
	if err == nil || strings.Contains(err.Error(), "secret-key") {
		t.Errorf("error leaks the key or is missing: %v", err)
	}
}
