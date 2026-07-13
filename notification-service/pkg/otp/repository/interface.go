package repository

import (
	"context"
	"time"

	"github.com/hiabhi-cpu/notification-service/pkg/otp/model"
)

// OTPStore handles Redis operations for OTPs and authenticated sessions.
type OTPStore interface {
	SaveOTPHash(ctx context.Context, refID string, hash string, mobile string, ttl time.Duration) error
	GetOTPHash(ctx context.Context, refID string) (hash string, mobile string, err error)
	DeleteOTP(ctx context.Context, refID string) error

	SaveSession(ctx context.Context, sessionID string, state model.SessionState, ttl time.Duration) error
	// GetSession returns the verified-OTP session, or nil if missing/expired.
	GetSession(ctx context.Context, sessionID string) (*model.SessionState, error)

	// IncrVerifyAttempts counts verification attempts for one reference ID so a
	// 6-digit OTP cannot be brute-forced. The counter shares the OTP's lifetime.
	IncrVerifyAttempts(ctx context.Context, refID string, ttl time.Duration) (int64, error)
	// AcquireSendCooldown returns false while the per-mobile resend cooldown is
	// still running (SET NX), true when this send may proceed.
	AcquireSendCooldown(ctx context.Context, mobile string, ttl time.Duration) (bool, error)
	// IncrHourlySends counts sends per mobile in a rolling hour — the guard
	// against SMS-pumping cost attacks.
	IncrHourlySends(ctx context.Context, mobile string) (int64, error)

	// SaveClaimOTP stores an OTP the same way SaveOTPHash does, plus an opaque
	// ref, and records refID in the hospital's active claim set.
	SaveClaimOTP(ctx context.Context, refID, hash, mobile, ref, hospitalID string, ttl time.Duration) error
	// GetClaimOTP returns the hash, mobile, and opaque ref for a claim OTP.
	GetClaimOTP(ctx context.Context, refID string) (hash, mobile, ref string, err error)
	// ClaimMembers returns the reference IDs currently claimed for a hospital.
	ClaimMembers(ctx context.Context, hospitalID string) ([]string, error)
	// RemoveClaim drops one reference ID from the hospital's claim set.
	RemoveClaim(ctx context.Context, hospitalID, refID string) error
	// IncrResolveAttempts counts code-resolve attempts per hospital (brute-force guard).
	IncrResolveAttempts(ctx context.Context, hospitalID string, ttl time.Duration) (int64, error)
}
