// Shapes returned by /admin/api (only the fields the UI uses).

export type Overrides = {
  profile?: string;
  max_comments?: number;
  min_severity?: string;
  model?: string;
  auto_review?: boolean;
  drafts?: boolean;
  instructions?: string;
};

export type Me = {
  email: string;
  name: string;
  csrf: string;
  version: string;
  profiles: string[];
  allowlist: boolean;
  require_reviewer: boolean;
};

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
  settings: Overrides;
  updated_at: string;
  updated_by: string;
};
