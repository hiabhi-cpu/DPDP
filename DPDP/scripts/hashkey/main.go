package main

import (
	"fmt"
	"os"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: hashkey <raw-api-key>")
		fmt.Fprintln(os.Stderr, "  Example: go run main.go TEST-HOSPITAL-API-KEY-LOCAL-DEV-001")
		os.Exit(1)
	}

	key := os.Args[1]
	hash, err := bcrypt.GenerateFromPassword([]byte(key), 12)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bcrypt error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(string(hash))
}
