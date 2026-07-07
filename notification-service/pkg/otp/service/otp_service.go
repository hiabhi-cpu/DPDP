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
}

const (
	otpExpiry     = 3 * time.Minute
	sessionExpiry = 15 * time.Minute
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

// Sentinel errors
var (
	ErrInvalidOTP = fmt.Errorf("invalid or expired OTP")
)
