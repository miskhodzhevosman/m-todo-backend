package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type config struct {
	Host        string
	Port        string
	DBPath      string
	CORSOrigins []string
}

func loadConfig() (config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return config{}, fmt.Errorf("load .env: %w", err)
	}
	cfg := config{
		Host:   envOrDefault("HOST", "127.0.0.1"),
		Port:   envOrDefault("PORT", "8000"),
		DBPath: envOrDefault("DB_PATH", "./todos.db"),
	}
	port, err := strconv.Atoi(cfg.Port)
	if err != nil || port < 1 || port > 65535 {
		return config{}, errors.New("PORT must be between 1 and 65535")
	}
	for _, origin := range strings.Split(envOrDefault("CORS_ORIGINS", "http://localhost:5173"), ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			cfg.CORSOrigins = append(cfg.CORSOrigins, origin)
		}
	}
	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
