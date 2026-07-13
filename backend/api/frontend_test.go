package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func TestFrontendFallbackAndStaticCaching(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dist := fstest.MapFS{
		"index.html":     {Data: []byte("<html>app shell</html>")},
		"assets/app.js": {Data: []byte("console.log('ok')")},
	}
	router := gin.New()
	registerFrontend(router, dist)

	tests := []struct {
		name        string
		path        string
		wantStatus  int
		wantBody    string
		wantCache   string
		rejectBody  string
	}{
		{
			name:       "client route falls back to index",
			path:       "/account-pool",
			wantStatus: http.StatusOK,
			wantBody:   "app shell",
			wantCache:  "no-cache",
		},
		{
			name:       "hashed asset is immutable",
			path:       "/assets/app.js",
			wantStatus: http.StatusOK,
			wantBody:   "console.log",
			wantCache:  "public, max-age=31536000, immutable",
		},
		{
			name:       "missing hashed asset is not html",
			path:       "/assets/old-hash.js",
			wantStatus: http.StatusNotFound,
			rejectBody: "app shell",
		},
		{
			name:       "missing icon is not html",
			path:       "/icon.svg",
			wantStatus: http.StatusNotFound,
			rejectBody: "app shell",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			router.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(recorder.Body.String(), tt.wantBody) {
				t.Fatalf("body %q does not contain %q", recorder.Body.String(), tt.wantBody)
			}
			if tt.rejectBody != "" && strings.Contains(recorder.Body.String(), tt.rejectBody) {
				t.Fatalf("body unexpectedly contains %q", tt.rejectBody)
			}
			if tt.wantCache != "" && recorder.Header().Get("Cache-Control") != tt.wantCache {
				t.Fatalf("cache-control = %q, want %q", recorder.Header().Get("Cache-Control"), tt.wantCache)
			}
		})
	}
}
