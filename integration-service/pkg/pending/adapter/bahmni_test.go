package adapter

import (
	"testing"
	"time"
)

func TestFromBahmni_MapsFullPayload(t *testing.T) {
	body := []byte(`{
		"patientId": "PA-00234",
		"givenName": "Asha",
		"familyName": "Rao",
		"phoneNumber": "+91 98765 43210",
		"birthdate": "1990-05-01"
	}`)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)

	got, err := FromBahmni(body, "hosp-1", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.HospitalID != "hosp-1" {
		t.Errorf("HospitalID = %q, want hosp-1", got.HospitalID)
	}
	if got.HMSPatientID != "PA-00234" {
		t.Errorf("HMSPatientID = %q, want PA-00234", got.HMSPatientID)
	}
	if got.Name != "Asha Rao" {
		t.Errorf("Name = %q, want 'Asha Rao'", got.Name)
	}
	if got.Mobile != "9876543210" {
		t.Errorf("Mobile = %q, want 9876543210 (normalized)", got.Mobile)
	}
	if got.DOB != "1990-05-01" {
		t.Errorf("DOB = %q, want 1990-05-01", got.DOB)
	}
	if got.RegisteredAt != "2026-07-13T10:00:00Z" {
		t.Errorf("RegisteredAt = %q", got.RegisteredAt)
	}
}

func TestFromBahmni_OmitsDOBWhenAbsent(t *testing.T) {
	body := []byte(`{"patientId":"PA-1","givenName":"A","familyName":"B","phoneNumber":"9876543210"}`)
	got, err := FromBahmni(body, "hosp-1", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DOB != "" {
		t.Errorf("DOB = %q, want empty", got.DOB)
	}
}

func TestFromBahmni_RejectsMissingMobile(t *testing.T) {
	body := []byte(`{"patientId":"PA-1","givenName":"A","familyName":"B"}`)
	if _, err := FromBahmni(body, "hosp-1", time.Now()); err == nil {
		t.Fatal("expected error for missing mobile, got nil")
	}
}

func TestFromBahmni_RejectsShortMobile(t *testing.T) {
	body := []byte(`{"patientId":"PA-1","givenName":"A","familyName":"B","phoneNumber":"12345"}`)
	if _, err := FromBahmni(body, "hosp-1", time.Now()); err == nil {
		t.Fatal("expected error for short mobile, got nil")
	}
}

func TestFromBahmni_RejectsMissingPatientID(t *testing.T) {
	body := []byte(`{"givenName":"A","familyName":"B","phoneNumber":"9876543210"}`)
	if _, err := FromBahmni(body, "hosp-1", time.Now()); err == nil {
		t.Fatal("expected error for missing patientId, got nil")
	}
}
