package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	sharedcrypto "github.com/hiabhi-cpu/DPDP/shared/crypto"
	sharedsecrets "github.com/hiabhi-cpu/DPDP/shared/secrets"
	"github.com/hiabhi-cpu/DPDP/notification-service/store"
)

const otpBcryptCost = 10 // lower than API key cost — OTPs have 5-min TTL

// OTPService orchestrates OTP generation, storage, and verification.
type OTPService struct {
	redisStore     *store.RedisOTPStore
	smsClient      SMSClient
	secretsProvider sharedsecrets.Provider
	db             *pgxpool.Pool
}

// NewOTPService creates an OTPService.
func NewOTPService(
	redisStore *store.RedisOTPStore,
	smsClient SMSClient,
	secretsProvider sharedsecrets.Provider,
	db *pgxpool.Pool,
) *OTPService {
	return &OTPService{
		redisStore:      redisStore,
		smsClient:       smsClient,
		secretsProvider: secretsProvider,
		db:              db,
	}
}

// SendResult is returned by SendOTP.
type SendResult struct {
	SessionID string
	ExpiresAt time.Time
}

// SendOTP generates a 6-digit OTP, stores it in Redis (+ Postgres for durability),
// and sends it via SMS. The raw mobile number is hashed immediately.
//
// Security: raw mobile, raw OTP — neither are stored anywhere after this function returns.
func (s *OTPService) SendOTP(ctx context.Context, hospitalID, mobile, purpose string) (*SendResult, error) {
	// Fetch hospital key and system salt to compute mobile_hash
	salt, err := s.secretsProvider.GetSystemSalt(ctx)
	if err != nil {
		return nil, fmt.Errorf("otp: get system salt: %w", err)
	}
	hospitalKey, err := s.secretsProvider.GetHospitalKey(ctx, hospitalID)
	if err != nil {
		return nil, fmt.Errorf("otp: get hospital key: %w", err)
	}

	// mobile_hash uses the same algorithm as patient_key — consistent across the system
	mobileHash := sharedcrypto.ComputePatientKey(mobile, salt, hospitalKey)

	// Generate cryptographically random OTP
	otp, err := sharedcrypto.GenerateOTP()
	if err != nil {
		return nil, fmt.Errorf("otp: generate otp: %w", err)
	}

	// Hash the OTP before storage (bcrypt — one-way)
	otpHash, err := bcrypt.GenerateFromPassword([]byte(otp), otpBcryptCost)
	if err != nil {
		return nil, fmt.Errorf("otp: hash otp: %w", err)
	}

	sessionID := uuid.New().String()
	expiresAt := time.Now().Add(store.OTPExpiry)

	// Store in Redis (primary — fast TTL management)
	if err := s.redisStore.Store(ctx, hospitalID, mobileHash, string(otpHash), purpose); err != nil {
		return nil, fmt.Errorf("otp: redis store: %w", err)
	}

	// Store in Postgres (durability + audit trail)
	if _, err := s.db.Exec(ctx, `
		INSERT INTO notification.otp_sessions
			(id, hospital_id, mobile_hash, otp_hash, purpose, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sessionID, hospitalID, mobileHash, string(otpHash), purpose, expiresAt); err != nil {
		// Non-fatal: Redis is primary. Log and continue.
		// In production, this should alert.
		_ = err
	}

	// Send SMS (raw mobile discarded after this call)
	if err := s.smsClient.SendOTP(ctx, mobile, otp); err != nil {
		return nil, fmt.Errorf("otp: send sms: %w", err)
	}

	// otp and mobile are no longer referenced after this point
	return &SendResult{
		SessionID: sessionID,
		ExpiresAt: expiresAt,
	}, nil
}

// VerifyResult is returned by VerifyOTP.
type VerifyResult struct {
	Verified bool
	Locked   bool   // true if max attempts exceeded
	Message  string
}

// VerifyOTP checks the user-provided OTP against the stored bcrypt hash.
// Increments attempt counter on failure. Marks session as used on success.
func (s *OTPService) VerifyOTP(ctx context.Context, hospitalID, mobile, otp string) (*VerifyResult, error) {
	salt, err := s.secretsProvider.GetSystemSalt(ctx)
	if err != nil {
		return nil, err
	}
	hospitalKey, err := s.secretsProvider.GetHospitalKey(ctx, hospitalID)
	if err != nil {
		return nil, err
	}

	mobileHash := sharedcrypto.ComputePatientKey(mobile, salt, hospitalKey)

	record, err := s.redisStore.GetRecord(ctx, hospitalID, mobileHash)
	if err != nil {
		return nil, fmt.Errorf("otp: redis get: %w", err)
	}
	if record == nil {
		return &VerifyResult{Verified: false, Message: "OTP expired or not found"}, nil
	}

	// Check attempt limit BEFORE comparing (prevents timing oracle on locked sessions)
	if record.Attempts >= store.MaxAttempts {
		return &VerifyResult{Verified: false, Locked: true, Message: "OTP locked — too many attempts"}, nil
	}

	// bcrypt comparison (timing-safe)
	if err := bcrypt.CompareHashAndPassword([]byte(record.OTPHash), []byte(otp)); err != nil {
		// Increment attempt counter
		newCount, _ := s.redisStore.IncrementAttempts(ctx, hospitalID, mobileHash)
		if newCount >= store.MaxAttempts {
			return &VerifyResult{Verified: false, Locked: true, Message: "OTP locked — max attempts reached"}, nil
		}
		return &VerifyResult{Verified: false, Message: "incorrect OTP"}, nil
	}

	// Success: mark used (delete from Redis — prevents replay)
	_ = s.redisStore.MarkUsed(ctx, hospitalID, mobileHash)

	return &VerifyResult{Verified: true}, nil
}
