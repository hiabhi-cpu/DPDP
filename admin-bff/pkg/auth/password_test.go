package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("s3cret-dev-pw")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "s3cret-dev-pw" || hash == "" {
		t.Fatal("hash must be a non-empty transformation of the password")
	}
	if !VerifyPassword(hash, "s3cret-dev-pw") {
		t.Fatal("VerifyPassword rejected the correct password")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("VerifyPassword accepted the wrong password")
	}
}
