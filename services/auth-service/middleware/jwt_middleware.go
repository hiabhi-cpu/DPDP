package middleware

import (
	"crypto/rsa"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	authservice "github.com/hiabhi-cpu/DPDP/auth-service/service"
)

// JWTMiddleware validates RS256 JWT tokens on every request and injects
// hospital_id into the Gin context. All downstream handlers read hospital_id
// from context — never from the request body or query parameters.
//
// This prevents hospital spoofing: a hospital JWT for H-001 cannot be used
// to access H-002's data, regardless of what the request body says.
func JWTMiddleware(publicKey *rsa.PublicKey) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing Authorization header",
				"hint":  "include 'Authorization: Bearer <token>' in your request",
			})
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid Authorization header format",
				"hint":  "expected 'Bearer <token>'",
			})
			return
		}

		tokenStr := parts[1]

		claims := &authservice.HospitalClaims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
			// Enforce RS256 — reject any token using a different algorithm (algorithm confusion attack)
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return publicKey, nil
		})

		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid or expired token",
			})
			return
		}

		// Inject hospital context — handlers MUST read from here, not request body
		c.Set("hospital_id", claims.HospitalID)
		c.Set("hospital_slug", claims.HospitalSlug)
		c.Set("hospital_role", claims.Role)

		c.Next()
	}
}

// MustGetHospitalID extracts hospital_id from Gin context.
// Panics if called outside the JWTMiddleware — this is intentional:
// a handler missing the middleware is a programming error, not a runtime condition.
func MustGetHospitalID(c *gin.Context) string {
	id, exists := c.Get("hospital_id")
	if !exists {
		panic("middleware.MustGetHospitalID: hospital_id not found in context — JWTMiddleware not applied?")
	}
	return id.(string)
}
