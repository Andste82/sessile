package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/Andste82/sessile/backend/internal/config"
)

// testRouter builds a router over a stand-in for the embedded build: one hashed
// asset, one unhashed file and the index that names them.
func testRouter(t *testing.T) http.Handler {
	t.Helper()
	dist := fstest.MapFS{
		"index.html":             {Data: []byte(`<!doctype html><script src="/assets/index-abc123.js">`)},
		"assets/index-abc123.js": {Data: []byte("console.log(1)")},
		"favicon.svg":            {Data: []byte("<svg/>")},
	}
	cfg := &config.Config{Shells: []string{"sh"}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(cfg, nil, nil, log, t.TempDir(), nil).Router(dist)
}

func get(t *testing.T, h http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// index.html is one URL whose contents name the current hashed bundles. Served
// without cache headers, a browser may apply a heuristic freshness lifetime and
// keep serving the previous build from its own cache after an upgrade — which
// is what left one browser running an old frontend against a new backend while
// another, which had refetched, behaved correctly.
func TestIndexIsRevalidated(t *testing.T) {
	h := testRouter(t)

	for _, path := range []string{"/", "/sessions/abc", "/settings"} {
		t.Run(path, func(t *testing.T) {
			rec := get(t, h, path, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != cacheRevalidate {
				t.Errorf("Cache-Control = %q, want %q", got, cacheRevalidate)
			}
			if rec.Header().Get("ETag") == "" {
				t.Error("no ETag: every reload would then refetch the whole document")
			}
		})
	}
}

// Revalidating on every navigation is only cheap if the unchanged answer is a
// 304 with no body.
func TestIndexRevalidationIsCheap(t *testing.T) {
	h := testRouter(t)

	first := get(t, h, "/", nil)
	tag := first.Header().Get("ETag")

	second := get(t, h, "/", map[string]string{"If-None-Match": tag})
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if body := second.Body.String(); body != "" {
		t.Errorf("304 carried a body: %q", body)
	}
	if got := second.Header().Get("Cache-Control"); got != cacheRevalidate {
		t.Errorf("Cache-Control on the 304 = %q, want %q", got, cacheRevalidate)
	}

	// A stale tag has to serve the new document rather than another 304.
	stale := get(t, h, "/", map[string]string{"If-None-Match": `"0000000000000000"`})
	if stale.Code != http.StatusOK {
		t.Errorf("stale ETag got %d, want 200", stale.Code)
	}
}

// Hashed assets are immutable by construction: a changed file is a changed URL.
// This is what keeps "revalidate the index" from meaning "refetch everything".
func TestHashedAssetsAreCachedForever(t *testing.T) {
	h := testRouter(t)

	rec := get(t, h, "/assets/index-abc123.js", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != cacheImmutable {
		t.Errorf("Cache-Control = %q, want %q", got, cacheImmutable)
	}
}

// Files the build leaves unhashed share index.html's problem — one URL, changing
// contents — so they get its answer.
func TestUnhashedFilesAreRevalidated(t *testing.T) {
	h := testRouter(t)

	rec := get(t, h, "/favicon.svg", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != cacheRevalidate {
		t.Errorf("Cache-Control = %q, want %q", got, cacheRevalidate)
	}
}

// The API must not be handed the SPA's caching, nor the SPA fallback.
func TestApiRoutesAreNotCachedAsTheSPA(t *testing.T) {
	h := testRouter(t)

	rec := get(t, h, "/api/nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got == cacheImmutable {
		t.Errorf("Cache-Control = %q, want the API not to be cached forever", got)
	}
}
