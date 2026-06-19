package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/httpapi/handlers"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type Server struct {
	router   *gin.Engine
	userRepo storage.UserRepo
	jwtMgr   *auth.JWTManager
	pool     *pgxpool.Pool
}

func NewServer(userRepo storage.UserRepo, jwtMgr *auth.JWTManager, pool *pgxpool.Pool) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(Recover(), RequestLog())

	s := &Server{router: r, userRepo: userRepo, jwtMgr: jwtMgr, pool: pool}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	r := s.router

	// Health
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/readyz", s.readyz)

	// Auth
	authHandler := handlers.NewAuthHandler(s.userRepo, s.jwtMgr)
	authGroup := r.Group("/api/v1/auth")
	{
		authGroup.POST("/register", authHandler.Register)
		authGroup.POST("/login", authHandler.Login)
		authGroup.POST("/refresh", AuthMiddleware(s.jwtMgr), authHandler.Refresh)
	}

	// Plan 2 will add: sessions, images
	// Plan 3 will add: questions, expert moderation
}

func (s *Server) readyz(c *gin.Context) {
	if err := s.pool.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

func (s *Server) Handler() http.Handler {
	return s.router
}
