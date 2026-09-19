package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Cyaside/Triovexa/internal/auth"
	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

func main() {
	username := flag.String("username", "", "username to create")
	roleValue := flag.String("role", domain.RoleAdmin, "viewer, operator, or admin")
	passwordEnv := flag.String("password-env", "TRIOVEXA_ADMIN_PASSWORD", "environment variable containing the password")
	flag.Parse()
	password := os.Getenv(*passwordEnv)
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" || strings.EqualFold(databaseURL, "memory") {
		fmt.Fprintln(os.Stderr, "DATABASE_URL must point to PostgreSQL")
		os.Exit(2)
	}
	role, err := auth.ParseRole(*roleValue)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	repository, err := storage.NewPostgresStore(databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer repository.Close()
	user, err := auth.NewService(repository, 0).CreateUser(context.Background(), *username, password, role)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("created %s user %s (%s)\n", user.Role, user.Username, user.ID)
}
