package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vlgrigoriev/coeus/internal/auth"
	"github.com/vlgrigoriev/coeus/internal/config"
	"github.com/vlgrigoriev/coeus/internal/httpapi/handlers"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
	"github.com/vlgrigoriev/coeus/internal/storage"
)

type Server struct {
	router       *gin.Engine
	userRepo     storage.UserRepo
	sessionRepo  storage.SessionRepo
	imageRepo    storage.ImageRepo
	questionRepo storage.QuestionRepo
	jobQueue     storage.JobQueue
	jwtMgr       *auth.JWTManager
	pool         *pgxpool.Pool
	uploadCfg    config.UploadConfig
	embedder     pipeline.AIEmbedder
}

func NewServer(
	userRepo storage.UserRepo,
	sessionRepo storage.SessionRepo,
	imageRepo storage.ImageRepo,
	questionRepo storage.QuestionRepo,
	jobQueue storage.JobQueue,
	jwtMgr *auth.JWTManager,
	pool *pgxpool.Pool,
	uploadCfg config.UploadConfig,
	embedder pipeline.AIEmbedder,
) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(Recover(), RequestLog())

	s := &Server{
		router: r, userRepo: userRepo, sessionRepo: sessionRepo,
		imageRepo: imageRepo, questionRepo: questionRepo, jobQueue: jobQueue,
		jwtMgr: jwtMgr, pool: pool, uploadCfg: uploadCfg, embedder: embedder,
	}
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

	// Sessions + Images (auth required)
	sessionHandler := handlers.NewSessionHandler(s.sessionRepo, s.imageRepo)
	imageHandler := handlers.NewImageHandler(s.imageRepo, s.jobQueue, s.uploadCfg)

	apiGroup := r.Group("/api/v1")
	apiGroup.Use(AuthMiddleware(s.jwtMgr))
	{
		sessions := apiGroup.Group("/sessions")
		{
			sessions.POST("", sessionHandler.Create)
			sessions.GET("", sessionHandler.List)
			sessions.GET("/:id", sessionHandler.Get)
			sessions.POST("/:id/close", sessionHandler.Close)

			// Image routes — SessionWindow guards ownership + expiry
			sessions.POST("/:id/images", SessionWindow(s.sessionRepo), imageHandler.Upload)
			sessions.GET("/:id/images", SessionWindow(s.sessionRepo), imageHandler.List)
		}

		// Questions — both roles; behavior splits inside the handler.
		// POST and PATCH are expert-only via per-route RoleGuard (spec §4.4).
		questionHandler := handlers.NewQuestionHandler(s.questionRepo, s.sessionRepo, s.embedder)
		questions := apiGroup.Group("/questions")
		{
			questions.GET("", questionHandler.List)
			questions.GET("/:id", questionHandler.Get)
			questions.POST("", RoleGuard("expert"), questionHandler.Create)
			questions.PATCH("/:id", RoleGuard("expert"), questionHandler.Update)
		}

		// Expert image access — expert only (spec §4.5).
		expertHandler := handlers.NewExpertHandler(s.imageRepo)
		expertImages := apiGroup.Group("/images", RoleGuard("expert"))
		{
			expertImages.GET("/:id", expertHandler.GetImage)
			expertImages.GET("/:id/verification-report", expertHandler.GetVerificationReport)
		}
	}
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
