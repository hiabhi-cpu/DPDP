export interface Purpose {
  key: string;
  label: string;
  description: string;
}

// ponytail: static in P1. A dynamic /kiosk/api/notice endpoint arrives in P2
// with multi-language/managed content (see the spec's out-of-scope list).
export const NOTICE_TEXT =
  "This hospital will process your personal and health data to provide care. " +
  "Under the Digital Personal Data Protection Act, your consent is required for " +
  "each purpose below. You may decline any purpose, and you can withdraw consent later.";

export const PURPOSES: Purpose[] = [
  { key: "treatment", label: "Treatment & care", description: "Use your data to diagnose and treat you." },
  { key: "records", label: "Medical records", description: "Maintain your medical history for continuity of care." },
  { key: "billing", label: "Billing & insurance", description: "Process payments and insurance claims." },
];
