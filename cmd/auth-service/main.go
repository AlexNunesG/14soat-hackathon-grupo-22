// Command auth-service implements registration, login, and health-check
// endpoints for the FIAP X video-processing system (Delivery 1 of
// .ai-agents/PLAN.md).
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/14SOAT-HACKATHON/app/internal/config"
	"github.com/14SOAT-HACKATHON/app/internal/db"
	"github.com/14SOAT-HACKATHON/app/internal/users"
)

func main() {
	cfg := config.Load()

	connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.NewPool(connectCtx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("auth-service: connecting to database: %v", err)
	}
	defer pool.Close()

	srv := &server{
		repo:      users.NewPostgresRepository(pool),
		jwtSecret: cfg.JWTSecret,
		jwtTTL:    time.Duration(cfg.JWTTTLHours) * time.Hour,
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Route("/auth", func(r chi.Router) {
		r.Post("/register", srv.handleRegister)
		r.Post("/login", srv.handleLogin)
		r.Get("/health", srv.handleHealth)
	})

	addr := ":" + cfg.Port
	log.Printf("auth-service: listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("auth-service: server error: %v", err)
	}
}
