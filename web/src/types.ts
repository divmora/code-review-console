// Shapes returned by /admin/api (only the fields the UI uses).

export type Overrides = {
  profile?: string;
  max_comments?: number;
  min_severity?: string;
  model?: string;
  auto_review?: boolean;
  drafts?: boolean;
  instructions?: string;
  diagram?: "off" | "auto" | "always";
  labels?: Labels;
};

// Labels are label suggestions (issue #162): set fields win over lower levels.
export type Labels = {
  mode?: "off" | "suggest" | "apply";
  allow?: string[];
  suggest_only?: string[];
  max?: number;
  instructions?: string;
};

export type Feature = "hosts" | "rules" | "learnings" | "executors" | "budgets" | "metrics" | "audit" | "branch_sync" | "sso_rules";

export type LicenseStatus = {
  state: "none" | "active" | "grace" | "expired" | "invalid";
  source?: string;
  id?: string;
  customer?: string;
  plan?: string;
  expires_at?: string;
  grace_until?: string;
  features: Feature[];
  error?: string;
  managed: boolean;
};

export type Me = {
  email: string;
  name: string;
  csrf: string;
  version: string;
  profiles: string[];
  allowlist: boolean;
  require_reviewer: boolean;
  license: LicenseStatus;
  free_retention_days: number;
  // Guided setup progress (required steps).
  setup?: { complete: boolean; done?: number; total?: number };
  // Why reviews can't run with the current license ("" = they can).
  reviews_paused?: string;
  // The license or host tokens that expire within two weeks, or have expired.
  expiry_warnings?: ExpiryWarning[];
  // admin, operator or viewer; and how many changes wait for approval
  // (all for an admin, their own for an operator).
  role?: "admin" | "operator" | "viewer";
  pending_approvals?: number;
};

export type ExpiryWarning = {
  kind: "license" | "host"; host_id?: number; name: string; expires: string; days: number;
  severity: "warn" | "bad"; message: string;
};

export type TokenExpiry = { expires?: string; known: boolean; checked?: string; error?: string };

export type Access = {
  enabled: boolean;
  source: string;
  require_reviewer: boolean;
  require_reviewer_source: string;
  skip_branch_sync: boolean;
  skip_branch_sync_source: string;
  score_labels: boolean;
  score_labels_source: string;
  commit_status: boolean;
  commit_status_source: string;
  approve: boolean;
  approve_source: string;
  rules?: string[];
  settings: Overrides;
};

export type Repo = {
  id: number;
  platform: string;
  host: string;
  path: string;
  mode: "inherit" | "enabled" | "disabled";
  settings: Overrides;
  last_seen?: string;
  updated_at: string;
  updated_by: string;
  effective: Access;
  // The host credential the repository uses (issue #136); absent: the
  // host's own token.
  credential?: { id: number; name: string; pattern: string };
  // The dashboards that pick the repository out (issue #155); those with
  // every repository aren't listed.
  dashboards?: { id: number; slug: string; title: string }[];
};

export type Rule = {
  id: number;
  platform: string;
  host: string;
  pattern: string;
  enabled: boolean | null;
  require_reviewer: boolean | null;
  skip_branch_sync: boolean | null;
  score_labels: boolean | null;
  commit_status: boolean | null;
  approve: boolean | null;
  settings: Overrides;
  updated_at: string;
  updated_by: string;
};
