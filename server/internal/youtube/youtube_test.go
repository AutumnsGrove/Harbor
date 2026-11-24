package youtube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The URL shapes are phase 7's id extraction too, so they are pinned here: a
// watch link with eleven tracking parameters and a bare youtu.be are the same
// video, and a playlist is not a video at all.
func TestVideoID(t *testing.T) {
	const id = "dQw4w9WgXcQ"
	tests := []struct {
		name string
		url  string
		want string
		ok   bool
	}{
		{"watch", "https://www.youtube.com/watch?v=" + id, id, true},
		{"watch with tracking", "https://www.youtube.com/watch?list=PL123&v=" + id + "&t=42s&si=xyz", id, true},
		{"watch, resuming at a timestamp", "https://m.youtube.com/watch?v=" + id + "&feature=youtu.be", id, true},
		{"music", "https://music.youtube.com/watch?v=" + id, id, true},
		{"short link", "https://youtu.be/" + id, id, true},
		{"short link with a timestamp", "https://youtu.be/" + id + "?t=30", id, true},
		{"shorts", "https://www.youtube.com/shorts/" + id, id, true},
		{"embed", "https://www.youtube.com/embed/" + id, id, true},
		{"live", "https://www.youtube.com/live/" + id, id, true},
		{"nocookie", "https://www.youtube-nocookie.com/embed/" + id, id, true},
		{"uppercase host", "https://WWW.YouTube.com/watch?v=" + id, id, true},
		{"playlist is not a video", "https://www.youtube.com/playlist?list=PL123", "", false},
		{"channel is not a video", "https://www.youtube.com/@someone", "", false},
		{"a video id of the wrong length", "https://www.youtube.com/watch?v=tooshort", "", false},
		{"a video id with a slash", "https://www.youtube.com/watch?v=abc/defghij", "", false},
		{"not youtube", "https://vimeo.com/123456", "", false},
		{"youtube as a path, not a host", "https://example.com/youtube.com/watch?v=" + id, "", false},
		{"empty", "", "", false},
		{"not a url", "just some words", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := VideoID(tc.url)
			if ok != tc.ok || got != tc.want {
				t.Errorf("VideoID(%q) = %q, %v; want %q, %v", tc.url, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The endpoint answers the shape Harbor stores, and a real answer is under a
// kilobyte — the test fixture is the shape, not the video.
const answer = `{
  "title": "Never Gonna Give You Up",
  "author_name": "Rick Astley",
  "author_url": "https://www.youtube.com/@RickAstleyYT",
  "thumbnail_url": "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg",
  "type": "video",
  "provider_url": "https://www.youtube.com/",
  "width": 200,
  "height": 113
}`

func TestMetadataReadsTheAnswer(t *testing.T) {
	var asked string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.String()
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "Harbor") {
			t.Errorf("User-Agent = %q, want an honest Harbor agent", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, UserAgent: "Harbor/test"})
	meta, err := client.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ?t=30")
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if meta.VideoID != "dQw4w9WgXcQ" || meta.Title != "Never Gonna Give You Up" || meta.Author != "Rick Astley" {
		t.Errorf("metadata = %+v", meta)
	}
	if !strings.HasPrefix(meta.Thumbnail, "https://i.ytimg.com/") {
		t.Errorf("thumbnail = %q", meta.Thumbnail)
	}
	// oEmbed refuses youtu.be, so the request is rewritten to the watch form.
	if !strings.Contains(asked, "url=https%3A%2F%2Fwww.youtube.com%2Fwatch%3Fv%3DdQw4w9WgXcQ") || !strings.Contains(asked, "format=json") {
		t.Errorf("asked %q, want a canonical watch URL and format=json", asked)
	}
}

func TestMetadataErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"a deleted video", http.StatusNotFound, `{"error":"Not Found"}`, "404"},
		{"a private video", http.StatusUnauthorized, `{}`, "401"},
		{"not JSON", http.StatusOK, "<html>", "not JSON"},
		{"no title", http.StatusOK, `{"author_name":"x"}`, "no title"},
		{"not a video type", http.StatusOK, `{"title":"x","type":"rich"}`, "type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client := New(Config{BaseURL: server.URL})
			_, err := client.Metadata(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestMetadataRefusesANonVideoURL(t *testing.T) {
	client := New(Config{BaseURL: "http://127.0.0.1:1"})
	if _, err := client.Metadata(context.Background(), "https://www.youtube.com/playlist?list=PL1"); err == nil {
		t.Fatal("a playlist URL was accepted")
	}
}

// Hostile JSON is a typed error, never a panic — the endpoint is the internet.
func FuzzParseAnswer(f *testing.F) {
	f.Add([]byte(answer))
	f.Add([]byte(`{}`))
	f.Add([]byte(""))
	f.Add([]byte(`{"title":null}`))
	f.Add([]byte(strings.Repeat("x", 4096)))

	f.Fuzz(func(t *testing.T, body []byte) {
		meta, err := parseAnswer("dQw4w9WgXcQ", body)
		if err != nil {
			return
		}
		// A success has to carry the id it was asked for and a title.
		if meta.VideoID != "dQw4w9WgXcQ" || strings.TrimSpace(meta.Title) == "" {
			t.Fatalf("answer accepted with %+v", meta)
		}
	})
}
