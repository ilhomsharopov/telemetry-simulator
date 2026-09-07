package handler

import (
	"net/http"
	"testing"
)

func TestAllowWebSocketOrigin(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "")
	req := httptestRequest("")
	if !allowWebSocketOrigin(req) {
		t.Fatal("empty ALLOWED_ORIGINS should allow all")
	}

	t.Setenv("ALLOWED_ORIGINS", "https://toir.tenzorsoft.uz, http://localhost:5173")
	if !allowWebSocketOrigin(httptestRequest("")) {
		t.Fatal("missing Origin should stay allowed for non-browser clients")
	}
	if !allowWebSocketOrigin(httptestRequest("https://toir.tenzorsoft.uz")) {
		t.Fatal("listed origin should be allowed")
	}
	if allowWebSocketOrigin(httptestRequest("https://evil.example")) {
		t.Fatal("unknown origin should be rejected")
	}
}

func httptestRequest(origin string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, "http://localhost/api/v1/ws", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}