package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hiabhi-cpu/notification-service/pkg/otp/model"
	"github.com/hiabhi-cpu/notification-service/pkg/otp/repository"
	sharedcrypto "github.com/hiabhi-cpu/shared/crypto"
)

// OTPService defines the business logic contract for OTP operations.
type OTPService interface {
	Send(ctx context.Context, mobile string) (*model.SendOTPResponse, error)
	Verify(ctx context.Context, req *model.VerifyOTPRequest) (sessionID string, err error)
	// ValidateSession reports whether sessionID is a live, OTP-verified session
	// for the given mobile. Called by consent-service (via /internal) before a
	// consent row is written — this is what makes otp_verified=true true.
	ValidateSession(ctx context.Context, sessionID, mobile string) error
	SendClaim(ctx context.Context, hospitalID, mobile, ref string) (*model.SendOTPResponse, error)
	ResolveClaim(ctx context.Context, hospitalID, otp string) (*model.ClaimResolveResult, error)
}

const (
	otpExpiry     = 3 * time.Minute
	sessionExpiry = 15 * time.Minute

	// Abuse guards: a 6-digit OTP survives ~5 guesses; SMS costs money.
	maxVerifyAttempts = 5
	sendCooldown      = 60 * time.Second
	maxSendsPerHour   = 5

	// ponytail: per-hospital code-resolve cap over the OTP window. Generous enough
	// for legit concurrent patients at pilot scale, low enough to throttle code
	// brute-forcing; raise per-hospital if a busy site trips it.
	maxResolveAttempts = 50
)

type otpService struct {
	repo      repository.OTPStore
	smsClient SMSClient
}

// NewOTPService creates an OTPService.
func NewOTPService(repo repository.OTPStore, smsClient SMSClient) OTPService {
	return &otpService{repo: repo, smsClient: smsClient}
}

func (s *otpService) Send(ctx context.Context, mobile string) (*model.SendOTPResponse, error) {
	// Hourly cap first (rejected sends still count), then the resend cooldown.
	sends, err := s.repo.IncrHourlySends(ctx, mobile)
	if err != nil {
		return nil, fmt.Errorf("service.Send: %w", err)
	}
	if sends > maxSendsPerHour {
		return nil, ErrTooManyRequests
	}
	ok, err := s.repo.AcquireSendCooldown(ctx, mobile, sendCooldown)
	if err != nil {
		return nil, fmt.Errorf("service.Send: %w", err)
	}
	if !ok {
		return nil, ErrTooManyRequests
	}

	otp, err := sharedcrypto.GenerateOTP()
	if err != nil {
		return nil, fmt.Errorf("service.Send: failed to generate OTP: %w", err)
	}

	hash, err := sharedcrypto.HashOTP(otp)
	if err != nil {
		return nil, fmt.Errorf("service.Send: failed to hash OTP: %w", err)
	}

	refID := uuid.New().String()

	if err := s.repo.SaveOTPHash(ctx, refID, hash, mobile, otpExpiry); err != nil {
		return nil, fmt.Errorf("service.Send: failed to save hash: %w", err)
	}

	// In background or sync depending on requirement
	if err := s.smsClient.SendOTP(ctx, mobile, otp); err != nil {
		// Log error but don't fail the request if saving succeeded, 
		// though in a real app you might want to handle this differently.
		return nil, fmt.Errorf("service.Send: failed to send SMS: %w", err)
	}

	return &model.SendOTPResponse{
		ReferenceID: refID,
		ExpiresAt:   time.Now().Add(otpExpiry),
	}, nil
}

func (s *otpService) Verify(ctx context.Context, req *model.VerifyOTPRequest) (string, error) {
	// Count the attempt BEFORE checking the code, and burn the OTP once the
	// budget is spent — a 6-digit code must never be brute-forceable.
	attempts, err := s.repo.IncrVerifyAttempts(ctx, req.ReferenceID, otpExpiry)
	if err != nil {
		return "", fmt.Errorf("service.Verify: %w", err)
	}
	if attempts > maxVerifyAttempts {
		_ = s.repo.DeleteOTP(ctx, req.ReferenceID)
		return "", ErrInvalidOTP
	}

	hash, storedMobile, err := s.repo.GetOTPHash(ctx, req.ReferenceID)
	if err != nil {
		return "", ErrInvalidOTP
	}

	if req.Mobile != storedMobile {
		return "", ErrInvalidOTP
	}

	if !sharedcrypto.VerifyOTP(req.OTP, hash) {
		return "", ErrInvalidOTP
	}

	// Delete OTP after successful verification to prevent reuse
	_ = s.repo.DeleteOTP(ctx, req.ReferenceID)

	sessionID := uuid.New().String()
	state := model.SessionState{
		Mobile:    req.Mobile,
		Verified:  true,
		ExpiresAt: time.Now().Add(sessionExpiry),
	}

	if err := s.repo.SaveSession(ctx, sessionID, state, sessionExpiry); err != nil {
		return "", fmt.Errorf("service.Verify: failed to save session: %w", err)
	}

	return sessionID, nil
}

// ValidateSession confirms a live verified session exists for (sessionID,
// mobile). The mobile must match the one the OTP was sent to, so a session
// obtained for one patient cannot vouch for another.
func (s *otpService) ValidateSession(ctx context.Context, sessionID, mobile string) error {
	state, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("service.ValidateSession: %w", err)
	}
	if state == nil || !state.Verified || state.Mobile != mobile {
		return ErrSessionNotVerified
	}
	return nil
}

func (s *otpService) SendClaim(ctx context.Context, hospitalID, mobile, ref string) (*model.SendOTPResponse, error) {
	// Same abuse guards as the walk-in send.
	sends, err := s.repo.IncrHourlySends(ctx, mobile)
	if err != nil {
		return nil, fmt.Errorf("service.SendClaim: %w", err)
	}
	if sends > maxSendsPerHour {
		return nil, ErrTooManyRequests
	}
	ok, err := s.repo.AcquireSendCooldown(ctx, mobile, sendCooldown)
	if err != nil {
		return nil, fmt.Errorf("service.SendClaim: %w", err)
	}
	if !ok {
		return nil, ErrTooManyRequests
	}

	// Generate a code unique within the hospital's active claim set, so an
	// entered code maps to at most one record.
	members, err := s.repo.ClaimMembers(ctx, hospitalID)
	if err != nil {
		return nil, fmt.Errorf("service.SendClaim: %w", err)
	}
	var otp, hash string
	for tries := 0; ; tries++ {
		if tries > 20 {
			return nil, fmt.Errorf("service.SendClaim: could not generate a unique code")
		}
		otp, err = sharedcrypto.GenerateOTP()
		if err != nil {
			return nil, fmt.Errorf("service.SendClaim: generate: %w", err)
		}
		if !s.codeCollides(ctx, members, otp) {
			break
		}
	}
	hash, err = sharedcrypto.HashOTP(otp)
	if err != nil {
		return nil, fmt.Errorf("service.SendClaim: hash: %w", err)
	}
	refID := uuid.New().String()
	if err := s.repo.SaveClaimOTP(ctx, refID, hash, mobile, ref, hospitalID, otpExpiry); err != nil {
		return nil, fmt.Errorf("service.SendClaim: save: %w", err)
	}
	if err := s.smsClient.SendOTP(ctx, mobile, otp); err != nil {
		return nil, fmt.Errorf("service.SendClaim: sms: %w", err)
	}
	return &model.SendOTPResponse{ReferenceID: refID, ExpiresAt: time.Now().Add(otpExpiry)}, nil
}

// codeCollides reports whether otp matches any active claim's hash.
func (s *otpService) codeCollides(ctx context.Context, members []string, otp string) bool {
	for _, refID := range members {
		hash, _, _, err := s.repo.GetClaimOTP(ctx, refID)
		if err != nil || hash == "" {
			continue
		}
		if sharedcrypto.VerifyOTP(otp, hash) {
			return true
		}
	}
	return false
}

func (s *otpService) ResolveClaim(ctx context.Context, hospitalID, otp string) (*model.ClaimResolveResult, error) {
	attempts, err := s.repo.IncrResolveAttempts(ctx, hospitalID, otpExpiry)
	if err != nil {
		return nil, fmt.Errorf("service.ResolveClaim: %w", err)
	}
	if attempts > maxResolveAttempts {
		return nil, ErrTooManyRequests
	}
	members, err := s.repo.ClaimMembers(ctx, hospitalID)
	if err != nil {
		return nil, fmt.Errorf("service.ResolveClaim: %w", err)
	}
	for _, refID := range members {
		hash, mobile, ref, err := s.repo.GetClaimOTP(ctx, refID)
		if err != nil {
			return nil, fmt.Errorf("service.ResolveClaim: %w", err)
		}
		if hash == "" { // expired; drop from the set
			_ = s.repo.RemoveClaim(ctx, hospitalID, refID)
			continue
		}
		if !sharedcrypto.VerifyOTP(otp, hash) {
			continue
		}
		// Match. Burn the OTP + claim, mint a verified session (same as Verify).
		_ = s.repo.DeleteOTP(ctx, refID)
		_ = s.repo.RemoveClaim(ctx, hospitalID, refID)
		sessionID := uuid.New().String()
		state := model.SessionState{Mobile: mobile, Verified: true, ExpiresAt: time.Now().Add(sessionExpiry)}
		if err := s.repo.SaveSession(ctx, sessionID, state, sessionExpiry); err != nil {
			return nil, fmt.Errorf("service.ResolveClaim: save session: %w", err)
		}
		return &model.ClaimResolveResult{SessionID: sessionID, Mobile: mobile, Ref: ref}, nil
	}
	return nil, ErrInvalidOTP
}

// Sentinel errors
var (
	ErrInvalidOTP = fmt.Errorf("invalid or expired OTP")
	// ErrTooManyRequests — send cooldown or hourly cap hit (HTTP 429).
	ErrTooManyRequests = fmt.Errorf("too many OTP requests")
	// ErrSessionNotVerified — session missing, expired, or mobile mismatch.
	ErrSessionNotVerified = fmt.Errorf("otp session not verified")
)
