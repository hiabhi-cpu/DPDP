// Package middleware provides reusable Gin HTTP middleware for the DPDP services.
package middleware

import (
	"crypto/rsa"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// HospitalClaims defines the JWT payload structure issued by auth-service.
// Every service uses this to extract hospital identity from a token.
type HospitalClaims struct {
	HospitalID   string `json:"hospital_id"`
	HospitalSlug string `json:"hospital_slug"`
	Role         string `json:"role"`
	jwt.RegisteredClaims
}

// Context keys — use these constants to read values set by JWTAuth.
const (
	CtxHospitalID   = "hospital_id"
	CtxHospitalSlug = "hospital_slug"
	CtxRole         = "role"
	CtxServiceName  = "service_name"
)

// RoleService is the role claim carried by internal service-to-service tokens.
const RoleService = "service"

// ServiceClaims is the JWT payload for internal service-to-service tokens issued
// by auth-service. Unlike HospitalClaims it carries NO hospital_id — a service
// acts across hospitals (e.g. the audit relay ships events for many hospitals).
type ServiceClaims struct {
	Role string `json:"role"` // always RoleService
	jwt.RegisteredClaims
}

// rsaKeyFunc returns a jwt.Keyfunc that enforces RS256 and rejects any other
// algorithm — this is the guard against the "alg:none" forgery attack. Shared by
// every token-verifying middleware so the guard lives in exactly one place.
func rsaKeyFunc(pubKey *rsa.PublicKey) jwt.Keyfunc {
	return func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return pubKey, nil
	}
}

// bearerToken extracts the raw token from an "Authorization: Bearer <token>"
// header. Returns false if the header is missing or malformed.
func bearerToken(c *gin.Context) (string, bool) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return "", false
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	return parts[1], true
}

// JWTAuth returns a Gin middleware that:
//  1. Reads the Bearer token from the Authorization header.
//  2. Validates the RS256 signature using the provided public key.
//  3. Sets hospital_id, hospital_slug, and role into the Gin context.
//
// If validation fails for any reason, the request is aborted with 401.
// hospital_id is ALWAYS extracted from the token — never from the request body —
// to prevent a compromised client from spoofing a different hospital's identity.
func JWTAuth(pubKey *rsa.PublicKey) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr, ok := bearerToken(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or malformed Authorization header"})
			return
		}

		claims := &HospitalClaims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, rsaKeyFunc(pubKey))
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		if claims.HospitalID == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token missing hospital_id claim"})
			return
		}

		// Set extracted claims into context for downstream handlers
		c.Set(CtxHospitalID, claims.HospitalID)
		c.Set(CtxHospitalSlug, claims.HospitalSlug)
		c.Set(CtxRole, claims.Role)

		c.Next()
	}
}

// InternalServiceAuth returns middleware that authenticates internal
// service-to-service calls (e.g. the consent-service audit relay → audit-service
// /internal endpoints). It verifies the RS256 signature via the same alg:none
// guard as JWTAuth and requires role == "service". It does NOT require a
// hospital_id claim, because a service token is not scoped to one hospital.
//
// Apply this to /internal route groups so a caller cannot reach them without a
// valid, auth-service-issued service token.
func InternalServiceAuth(pubKey *rsa.PublicKey) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr, ok := bearerToken(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or malformed Authorization header"})
			return
		}

		claims := &ServiceClaims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, rsaKeyFunc(pubKey))
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired service token"})
			return
		}

		if claims.Role != RoleService {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not a service token"})
			return
		}

		c.Set(CtxServiceName, claims.Subject)
		c.Next()
	}
}

// LoadPublicKey reads an RSA public key from a PEM file at the given path.
// Call this once at service startup and pass the result to JWTAuth.
func LoadPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("middleware.LoadPublicKey: failed to read %q: %w", path, err)
	}
	key, err := jwt.ParseRSAPublicKeyFromPEM(data)
	if err != nil {
		return nil, fmt.Errorf("middleware.LoadPublicKey: failed to parse PEM from %q: %w", path, err)
	}
	return key, nil
}
