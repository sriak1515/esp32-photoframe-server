package photoframe

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPushPaletteUsesExistingEndpoint(t *testing.T) {
	var method, path, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	client := &Client{host: "frame.local", resolvedIP: host, httpClient: server.Client()}
	if err := client.PushPalette([]byte(`{"black":{"r":1}}`)); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/api/settings/palette" || body != `{"black":{"r":1}}` {
		t.Fatalf("request = %s %s %s", method, path, body)
	}
}
