// Command provision-demo creates two fresh logical voting databases in a
// dedicated demo PostgreSQL instance. DATABASE_URL must point at its control DB.
// Credentials are read from environment and never printed.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("set DATABASE_URL to the dedicated demo control database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return errors.New("demo database connection failed; check URL, TLS and access")
	}
	defer conn.Close(context.Background())
	status := map[string]string{}
	for _, name := range []string{"votes_0", "votes_1"} {
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)", name).Scan(&exists); err != nil {
			return errors.New("could not inspect demo databases")
		}
		if exists {
			status[name] = "existing"
			continue
		}
		_, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
		if err != nil {
			var pgerr *pgconn.PgError
			if errors.As(err, &pgerr) && pgerr.Code == "42P04" {
				status[name] = "existing"
				continue
			}
			return errors.New("could not create demo voting database; verify CREATEDB permission")
		}
		status[name] = "created"
	}
	var version, fsync string
	if err := conn.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil {
		return errors.New("could not read server version")
	}
	if err := conn.QueryRow(ctx, "SHOW fsync").Scan(&fsync); err != nil {
		return errors.New("could not read durability setting")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"databases": status, "server_version": version, "fsync": fsync})
}
