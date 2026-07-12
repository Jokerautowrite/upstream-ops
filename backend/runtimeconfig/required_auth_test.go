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
