package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/vlgrigoriev/coeus/internal/config"
)

// newCORSTestEngine mirrors NewServer's middleware order (cors mounted before the
// auth-protected group) without needing a DB pool. The sentinel enforces that any
// non-preflight request reaching /api/v1 without a token is rejected 401 — proving
// the cors preflight short-circuits BEFORE auth would run.
func newCORSTestEngine(cfg config.CORSConfig) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.AllowedOrigins,
		AllowMethods:     cfg.AllowedMethods,
		AllowHeaders:     cfg.AllowedHeaders,
		ExposeHeaders:    cfg.ExposeHeaders,
		AllowCredentials: cfg.AllowCredentials,
		MaxAge:           cfg.MaxAge,
	}))
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	api := r.Group("/api/v1")
	api.Use(func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "unauthorized"}})
	})
	api.POST("/questions", func(c *gin.Context) { c.Status(http.StatusCreated) })
	return r
}

func TestCORS_PreflightReturns204Not401(t *testing.T) {
	r := newCORSTestEngine(config.CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedMethods: []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type", "X-Request-Id"},
		ExposeHeaders:  []string{"X-Request-Id"},
		MaxAge:         12 * time.Hour,
	})
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/questions", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight: got %d want 204 (must not reach auth sentinel -> 401)", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin: got %q want https://app.example.com", got)
	}
}

func TestCORS_HealthzEchoesOriginOnSimpleRequest(t *testing.T) {
	r := newCORSTestEngine(config.CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedMethods: []string{"GET"},
	})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("healthz: got %d want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin on simple request: got %q want https://app.example.com", got)
	}
}
