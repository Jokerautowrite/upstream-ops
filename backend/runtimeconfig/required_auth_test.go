package runtimeconfig

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/auth"
	"github.com/gin-gonic/gin"
)

func TestRequiredAuthMiddlewareRejectsOpenMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := &Manager{}
	router := gin.New()
	router.GET("/protected", manager.RequiredAuthMiddleware(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestRequiredAuthMiddlewareAcceptsValidAdministratorToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authService, err := auth.New("admin", "password", "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	token, _, err := authService.Login("admin", "password")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	manager := &Manager{auth: authService}
	router := gin.New()
	router.GET("/protected", manager.RequiredAuthMiddleware(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestRequiredAuthMiddlewareProtectsSub2PoolRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authService, err := auth.New("admin", "password", "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	token, _, err := authService.Login("admin", "password")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	manager := &Manager{auth: authService}
	router := gin.New()
	group := router.Group("/api")
	group.Use(manager.RequiredAuthMiddleware())
	group.GET("/sub2-pool/targets", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	tests := []struct {
		name       string
		token      string
		wantStatus int
	}{
		{name: "missing token", wantStatus: http.StatusUnauthorized},
		{name: "invalid token", token: "invalid", wantStatus: http.StatusUnauthorized},
		{name: "administrator token", token: token, wantStatus: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/sub2-pool/targets", nil)
			if tt.token != "" {
				request.Header.Set("Authorization", "Bearer "+tt.token)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
		})
	}
}
