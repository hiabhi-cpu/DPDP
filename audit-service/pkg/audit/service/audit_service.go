package service

import (
	"context"
	"fmt"

	"github.com/hiabhi-cpu/audit-service/pkg/audit/model"
	"github.com/hiabhi-cpu/audit-service/pkg/audit/repository"
)

// AuditService defines the business logic contract for the audit log.
type AuditService interface {
	LogEvent(ctx context.Context, event *model.AuditEvent) error
	GetLogs(ctx context.Context, filter model.AuditLogFilter) (*model.AuditLogPage, error)
}

type auditService struct {
	repo repository.AuditRepository
}

// New creates an AuditService.
func New(repo repository.AuditRepository) AuditService {
	return &auditService{repo: repo}
}

func (s *auditService) LogEvent(ctx context.Context, event *model.AuditEvent) error {
	if event.HospitalID == "" {
		return fmt.Errorf("service.LogEvent: hospital_id is required")
	}
	if event.EventType == "" {
		return fmt.Errorf("service.LogEvent: event_type is required")
	}
	if event.ActorID == "" {
		return fmt.Errorf("service.LogEvent: actor_id is required")
	}

	if err := s.repo.Insert(ctx, event); err != nil {
		return fmt.Errorf("service.LogEvent: %w", err)
	}

	return nil
}

func (s *auditService) GetLogs(ctx context.Context, filter model.AuditLogFilter) (*model.AuditLogPage, error) {
	if filter.HospitalID == "" {
		return nil, fmt.Errorf("service.GetLogs: hospital_id is required")
	}

	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.Limit < 1 {
		filter.Limit = 50
	} else if filter.Limit > 100 {
		filter.Limit = 100
	}

	events, total, err := s.repo.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("service.GetLogs: %w", err)
	}

	return &model.AuditLogPage{
		Events: events,
		Total:  total,
		Page:   filter.Page,
		Limit:  filter.Limit,
	}, nil
}
