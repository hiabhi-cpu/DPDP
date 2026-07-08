package model

// ConsentStats is the read-only aggregate returned by GET /api/v1/consent/stats.
// All counts are hospital-scoped by RLS. "pending review" is intentionally absent:
// consent_vault is append-only so its dpo_review_status is frozen; the live pending
// count comes from emergency-service's GET /api/v1/emergency/pending instead.
type ConsentStats struct {
	Consents  StatusCounts       `json:"consents"`
	ByPurpose []PurposeBreakdown `json:"by_purpose"`
	Activity  ActivityCounts     `json:"activity"`
	Emergency EmergencyCounts    `json:"emergency"`
}

// StatusCounts counts patients by the aggregate status of their latest consent row.
type StatusCounts struct {
	Active        int `json:"active"`
	Withdrawn     int `json:"withdrawn"`
	TotalPatients int `json:"total_patients"`
}

// PurposeBreakdown is the active/withdrawn tally for one purpose across latest rows.
type PurposeBreakdown struct {
	Purpose   string `json:"purpose"`
	Active    int    `json:"active"`
	Withdrawn int    `json:"withdrawn"`
}

// ActivityCounts counts rows written inside the window, by consent event type.
type ActivityCounts struct {
	WindowDays  int `json:"window_days"`
	Captures    int `json:"captures"`
	Withdrawals int `json:"withdrawals"`
	Renewals    int `json:"renewals"`
}

// EmergencyCounts counts immutable emergency-override rows in the vault.
type EmergencyCounts struct {
	Overrides int `json:"overrides"`
}
