// Command seedadmin inserts one admin user with a bcrypt password hash.
// Usage: EMAIL=admin@testhospital.local PASSWORD=admin-dev-password \
//        HOSPITAL_ID=a1b2c3d4-e5f6-7890-abcd-ef1234567890 DATABASE_URL=... \
//        go run ./cmd/seedadmin
package main

import (
	"context"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hiabhi-cpu/admin-bff/pkg/auth"
)

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("seedadmin: %s is required", k)
	}
	return v
}

func main() {
	ctx := context.Background()
	email := mustEnv("EMAIL")
	password := mustEnv("PASSWORD")
	hospitalID := mustEnv("HOSPITAL_ID")
	role := os.Getenv("ROLE")
	if role == "" {
		role = "admin"
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Fatalf("seedadmin: hash: %v", err)
	}

	pool, err := pgxpool.New(ctx, mustEnv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("seedadmin: db: %v", err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx,
		`INSERT INTO auth.admin_users (hospital_id, email, password_hash, role)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (email) DO NOTHING`,
		hospitalID, email, hash, role)
	if err != nil {
		log.Fatalf("seedadmin: insert: %v", err)
	}
	log.Printf("seedadmin: ensured admin %s (role=%s)", email, role)
}
