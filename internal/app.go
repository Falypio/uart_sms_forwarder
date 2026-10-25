package internal

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/dushixiang/uart_sms_forwarder/config"
	"github.com/dushixiang/uart_sms_forwarder/internal/fleet"
	"github.com/dushixiang/uart_sms_forwarder/internal/handler"
	"github.com/dushixiang/uart_sms_forwarder/internal/middleware"
	"github.com/dushixiang/uart_sms_forwarder/internal/service"
	"github.com/dushixiang/uart_sms_forwarder/internal/version"
	"github.com/dushixiang/uart_sms_forwarder/web"
	"github.com/go-orz/orz"
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	echomiddleware "github.com/labstack/echo/v5/middleware"
)

func Run(configPath string) {
	if err := orz.Quick(configPath, setup); err != nil {
		log.Fatal(err)
	}
}

func setup(app *orz.App) error {
	logger, db := app.Logger(), app.GetDatabase()
	var cfg config.AppConfig
	if c := app.GetConfig(); c != nil {
		if err := c.App.Unmarshal(&cfg); err != nil {
			return err
		}
	}
	if cfg.JWT.Secret == "" {
		cfg.JWT.Secret = uuid.NewString()
		logger.Warn("JWT secret not configured; generated an ephemeral secret")
	}
	if cfg.JWT.ExpiresHours <= 0 {
		cfg.JWT.ExpiresHours = 168
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	// SQLite has one writer. Serialize connections instead of returning SQLITE_BUSY under concurrent device events.
	sqlDB.SetMaxOpenConns(1)
	manager, err := fleet.New(db, cfg, logger)
	if err != nil {
		return err
	}
	oidc := service.NewOIDCService(logger, &cfg)
	auth := handler.NewAuthHandler(logger, service.NewAccountService(logger, oidc, &cfg))
	e := app.GetEcho()
	e.Use(echomiddleware.BodyLimit(65536))
	e.Use(echomiddleware.StaticWithConfig(echomiddleware.StaticConfig{
		Skipper: func(c *echo.Context) bool {
			return strings.HasPrefix(c.Request().URL.Path, "/api") || strings.HasPrefix(c.Request().URL.Path, "/health")
		},
		Index: "index.html", HTML5: true, Filesystem: web.Assets(),
	}))
	e.POST("/api/login", auth.Login)
	e.GET("/api/auth/config", auth.GetAuthConfig)
	e.GET("/api/auth/oidc/url", auth.GetOIDCAuthURL)
	e.POST("/api/auth/oidc/callback", auth.OIDCCallback)
	api := e.Group("/api")
	api.Use(middleware.JWTMiddleware(cfg.JWT.Secret, logger))
	api.GET("/version", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"version": version.GetVersion()})
	})
	manager.Register(api)
	e.GET("/health", func(c *echo.Context) error { return c.JSON(http.StatusOK, map[string]string{"status": "ok"}) })
	// Original unscoped /api/messages, /api/serial, /api/properties and /api/scheduled-tasks routes
	// are deliberately not mounted. Every business operation now requires a selected device.
	return manager.Start(context.Background())
}
