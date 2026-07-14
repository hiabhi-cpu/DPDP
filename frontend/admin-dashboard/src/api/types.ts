export interface StatusCounts { active: number; withdrawn: number; total_patients: number; }
export interface PurposeBreakdown { purpose: string; active: number; withdrawn: number; }
export interface ActivityCounts { window_days: number; captures: number; withdrawals: number; renewals: number; }
export interface EmergencyCounts { overrides: number; }
export interface ConsentStats {
  consents: StatusCounts;
  by_purpose: PurposeBreakdown[];
  activity: ActivityCounts;
  emergency: EmergencyCounts;
}

export interface AuditEvent {
  id: number;
  event_type: string;
  actor_id: string;
  actor_type: string;
  patient_key: string;
  ip_address: string;
  details: Record<string, unknown>;
  created_at: string;
}
export interface AuditLogPage { events: AuditEvent[]; total: number; page: number; limit: number; }

export interface ReviewItem {
  access_id: string;
  emergency_id: string;
  doctor_id: string;
  emergency_reason: string;
  clinical_note: string;
  hms_patient_id?: string;
  review_status: string;
  dpo_deadline: string;
  overdue: boolean;
  created_at: string;
}
export interface EmergencyPending { pending: ReviewItem[]; total: number; }

export interface Me { email: string; role: string; }

export interface PendingRow {
  hms_patient_id: string;
  name: string;
  mobile: string; // masked by the server
  status: "PENDING" | "CODE_SENT" | "DONE";
  registered_at: string;
}
