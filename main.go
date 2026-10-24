package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	db, err := openDatabase(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(os.Args) > 1 && os.Args[1] == "create-user" {
		flags := flag.NewFlagSet("create-user", flag.ContinueOnError)
		username := flags.String("username", "", "Username")
		claim := flags.Bool("claim-legacy", false, "Assign previously unowned tasks and categories to this user")
		if err := flags.Parse(os.Args[2:]); err != nil {
			return err
		}
		password, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
		if err != nil {
			return err
		}
		u, err := createUser(db, *username, strings.TrimRight(string(password), "\r\n"), *claim)
		if err != nil {
			return err
		}
		fmt.Printf("Created user %s (id=%d)\n", u.Username, u.ID)
		return nil
	}

	server := &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, cfg.Port),
		Handler:           newHandler(db, cfg.CORSOrigins, cfg.CookieSecure),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() {
		log.Printf("Todo API listening on http://%s", server.Addr)
		serveErr <- server.ListenAndServe()
	}()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
