package qbit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewTrimsTheTrailingSlash(t *testing.T) {
	c, err := New("http://qbit:8080/", "u", "p")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if c.baseURL != "http://qbit:8080" {
		t.Fatalf("got %q, want the trailing slash trimmed", c.baseURL)
	}
}

func TestHostOnly(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://qbit:8080", "qbit"},
		{"https://box.example.com/path", "box.example.com"},
		{"http://10.0.0.5:9091", "10.0.0.5"},
		{"not a url", "not a url"},
		{"", ""},
	} {
		if got := hostOnly(tc.in); got != tc.want {
			t.Fatalf("hostOnly(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFlattenHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-One", "a")
	got := flattenHeaders(h)
	if !strings.Contains(got, "X-One=a") {
		t.Fatalf("got %q, want the header included", got)
	}
	if flattenHeaders(http.Header{}) != "" {
		t.Fatal("an empty header set produced output")
	}
}

func TestLoginSucceedsAndKeepsTheCookie(t *testing.T) {
	var loginBody string
	var infoCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_ = r.ParseForm()
			loginBody = r.Form.Encode()
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "session", Path: "/"})
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if c, err := r.Cookie("SID"); err == nil {
				infoCookie = c.Value
			}
			_ = json.NewEncoder(w).Encode([]Torrent{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, "admin", "hunter2")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(loginBody, "username=admin") || !strings.Contains(loginBody, "password=hunter2") {
		t.Fatalf("got login form %q", loginBody)
	}
	if _, err := c.TorrentsByHashes(context.Background(), []string{"abc"}); err != nil {
		t.Fatalf("torrents: %v", err)
	}
	if infoCookie != "session" {
		t.Fatalf("got cookie %q on the follow-up call, want the login session reused", infoCookie)
	}
}

func loginServer(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "u", "p")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return c
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	err := loginServer(t, http.StatusOK, "Fails.").Login(context.Background())
	if err == nil {
		t.Fatal("a Fails. body was treated as a successful login")
	}
	if !strings.Contains(err.Error(), "wrong username or password") {
		t.Fatalf("got %v, want the credential hint", err)
	}
}

func TestLoginExplainsA403(t *testing.T) {
	err := loginServer(t, http.StatusForbidden, "").Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "IP-banned") {
		t.Fatalf("got %v, want the ban explanation", err)
	}
}

func TestLoginExplainsA401(t *testing.T) {
	err := loginServer(t, http.StatusUnauthorized, "nope").Login(context.Background())
	if err == nil {
		t.Fatal("a 401 was accepted")
	}
	if !strings.Contains(err.Error(), "Server domains") {
		t.Fatalf("got %v, want the server-domains hint", err)
	}
}

func TestLoginReportsAnUnexpectedStatus(t *testing.T) {
	err := loginServer(t, http.StatusInternalServerError, "boom").Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status=500") {
		t.Fatalf("got %v, want the status reported", err)
	}
}

func TestLoginAcceptsAnyTwoHundred(t *testing.T) {
	if err := loginServer(t, http.StatusNoContent, "").Login(context.Background()); err != nil {
		t.Fatalf("got %v, want a 2xx accepted", err)
	}
}

func torrentServer(t *testing.T, torrents []Torrent) (*Client, *string) {
	t.Helper()
	var lastQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.Query().Get("hashes")
		_ = json.NewEncoder(w).Encode(torrents)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "u", "p")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return c, &lastQuery
}

func TestTorrentsByHashesIsEmptyForNoHashes(t *testing.T) {
	c, query := torrentServer(t, nil)
	got, err := c.TorrentsByHashes(context.Background(), nil)
	if err != nil {
		t.Fatalf("torrents: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want an empty map", got)
	}
	if *query != "" {
		t.Fatal("an empty hash list still hit the server")
	}
}

func TestTorrentsByHashesLowercasesAndJoins(t *testing.T) {
	c, query := torrentServer(t, []Torrent{
		{Hash: "AABB", Name: "one", State: "uploading", Progress: 1, Category: "tv", SavePath: "/dl"},
	})
	got, err := c.TorrentsByHashes(context.Background(), []string{"AABB", "CcDd"})
	if err != nil {
		t.Fatalf("torrents: %v", err)
	}
	if *query != "aabb|ccdd" {
		t.Fatalf("got query %q, want lowercase pipe-joined hashes", *query)
	}
	tor, ok := got["aabb"]
	if !ok {
		t.Fatalf("got %v, want the torrent keyed by lowercase hash", got)
	}
	if tor.Name != "one" || tor.State != "uploading" || tor.Category != "tv" {
		t.Fatalf("got %+v", tor)
	}
	if _, ok := got["ccdd"]; ok {
		t.Fatal("a hash qbit did not return came back anyway")
	}
}

func TestTorrentByHashIsCaseInsensitive(t *testing.T) {
	c, _ := torrentServer(t, []Torrent{{Hash: "aabb", Name: "one"}})
	tor, err := c.TorrentByHash(context.Background(), "AABB")
	if err != nil {
		t.Fatalf("torrent: %v", err)
	}
	if tor == nil || tor.Name != "one" {
		t.Fatalf("got %+v, want the torrent found", tor)
	}
}

func TestTorrentByHashReturnsNilWhenTheTorrentIsGone(t *testing.T) {
	c, _ := torrentServer(t, []Torrent{})
	tor, err := c.TorrentByHash(context.Background(), "aabb")
	if err != nil {
		t.Fatalf("torrent: %v", err)
	}
	if tor != nil {
		t.Fatalf("got %+v, want nil for a removed torrent", tor)
	}
}

func TestTorrentsByHashesSurfacesANonOkStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c, err := New(srv.URL, "u", "p")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := c.TorrentsByHashes(context.Background(), []string{"a"}); err == nil {
		t.Fatal("a 403 was reported as success")
	}
}

func TestTorrentsByHashesSurfacesBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "u", "p")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := c.TorrentsByHashes(context.Background(), []string{"a"}); err == nil {
		t.Fatal("a malformed body was accepted")
	}
}

func TestClientFailsOnAnUnreachableHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	c, err := New(url, "u", "p")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := c.Login(context.Background()); err == nil {
		t.Fatal("a closed server was reported as a successful login")
	}
	if _, err := c.TorrentsByHashes(context.Background(), []string{"a"}); err == nil {
		t.Fatal("a closed server was reported as a successful lookup")
	}
}
