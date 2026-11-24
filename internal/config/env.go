package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvVar is one startup setting read from the environment. Environment
// variables win over the config file, so the console can run with no
// file at all (containers, Kubernetes, systemd).
type EnvVar struct {
	Name string // environment variable
	Key  string // the config file key it sets
	Help string
	set  func(c *Config, v string) error
}

// EnvVars lists the startup settings that can come from the environment.
// The CODEREVIEW_CONSOLE_ prefix also keeps them out of review jobs: no
// model profile or extra job variable may name them.
var EnvVars = []EnvVar{
	{"DATABASE_URL", "database.url", "PostgreSQL connection string (required)", func(c *Config, v string) error { c.Database.URL = v; return nil }},
	{"CODEREVIEW_CONSOLE_ADDR", "server.addr", "listen address, default :8080", func(c *Config, v string) error { c.Server.Addr = v; return nil }},
	{"CODEREVIEW_CONSOLE_TRUSTED_PROXIES", "server.trusted_proxies", "the load balancer's own IPs (comma-separated), to record client IPs; never a range clients connect from", func(c *Config, v string) error {
		c.Server.TrustedProxies = list(v)
		return nil
	}},
	{"CODEREVIEW_CONSOLE_TRUSTED_PROXY_HOPS", "server.trusted_proxy_hops", "instead of addresses: how many proxies are in front (usually 1)", func(c *Config, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("want a number, got %q", v)
		}
		c.Server.TrustedProxyHops = n
		return nil
	}},
	{"CODEREVIEW_CONSOLE_BASE_URL", "admin.base_url", "public URL of the console (sign-in redirect, webhook URLs)", func(c *Config, v string) error { c.Admin.BaseURL = v; return nil }},
	{"CODEREVIEW_CONSOLE_ADMIN_ENABLED", "admin.enabled", "true or false; default true when an OIDC issuer is set", func(c *Config, v string) error { return setBool(&c.Admin.Enabled, v) }},
	{"CODEREVIEW_CONSOLE_SESSION_TTL", "admin.session_ttl", "admin session length, default 12h", func(c *Config, v string) error { return setDuration(&c.Admin.SessionTTL, v) }},
	{"CODEREVIEW_CONSOLE_COOKIE_SECRET", "admin.cookie_secret", "32+ random characters, for several replicas", func(c *Config, v string) error { c.Admin.CookieSecret = v; return nil }},
	{"CODEREVIEW_CONSOLE_HSTS", "admin.hsts.enabled", "true or false, default true: send Strict-Transport-Security when base_url is https", func(c *Config, v string) error {
		var b bool
		if err := setBool(&b, v); err != nil {
			return err
		}
		c.Admin.HSTS.Enabled = &b
		return nil
	}},
	{"CODEREVIEW_CONSOLE_HSTS_MAX_AGE", "admin.hsts.max_age", "default 8760h (a year)", func(c *Config, v string) error { return setDuration(&c.Admin.HSTS.MaxAge, v) }},
	{"CODEREVIEW_CONSOLE_HSTS_INCLUDE_SUBDOMAINS", "admin.hsts.include_subdomains", "true or false, default false", func(c *Config, v string) error { return setBool(&c.Admin.HSTS.IncludeSubDomains, v) }},
	{"CODEREVIEW_CONSOLE_OIDC_ISSUER", "admin.oidc.issuer", "OpenID Connect issuer URL", func(c *Config, v string) error { c.Admin.OIDC.Issuer = v; return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_CLIENT_ID", "admin.oidc.client_id", "", func(c *Config, v string) error { c.Admin.OIDC.ClientID = v; return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_CLIENT_SECRET", "admin.oidc.client_secret", "", func(c *Config, v string) error { c.Admin.OIDC.ClientSecret = v; return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_SCOPES", "admin.oidc.scopes", "comma-separated, default openid,email,profile", func(c *Config, v string) error { c.Admin.OIDC.Scopes = list(v); return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_ALLOWED_EMAILS", "admin.oidc.allowed_emails", "comma-separated", func(c *Config, v string) error { c.Admin.OIDC.AllowedEmails = list(v); return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_ALLOWED_DOMAINS", "admin.oidc.allowed_domains", "comma-separated (license: sso_rules)", func(c *Config, v string) error { c.Admin.OIDC.AllowedDomains = list(v); return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_ALLOWED_GROUPS", "admin.oidc.allowed_groups", "comma-separated (license: sso_rules)", func(c *Config, v string) error { c.Admin.OIDC.AllowedGroups = list(v); return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_OPERATOR_EMAILS", "admin.oidc.operator_emails", "operators, comma-separated (license: sso_rules)", func(c *Config, v string) error { c.Admin.OIDC.OperatorEmails = list(v); return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_OPERATOR_GROUPS", "admin.oidc.operator_groups", "operator groups, comma-separated (license: sso_rules)", func(c *Config, v string) error { c.Admin.OIDC.OperatorGroups = list(v); return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_VIEWER_EMAILS", "admin.oidc.viewer_emails", "read-only users, comma-separated (license: sso_rules)", func(c *Config, v string) error { c.Admin.OIDC.ViewerEmails = list(v); return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_VIEWER_GROUPS", "admin.oidc.viewer_groups", "read-only groups, comma-separated (license: sso_rules)", func(c *Config, v string) error { c.Admin.OIDC.ViewerGroups = list(v); return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_ALLOWED_SUBJECTS", "admin.oidc.allowed_subjects", "admins by subject_claim, comma-separated", func(c *Config, v string) error { c.Admin.OIDC.AllowedSubjects = list(v); return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_OPERATOR_SUBJECTS", "admin.oidc.operator_subjects", "operators by subject_claim, comma-separated (license: sso_rules)", func(c *Config, v string) error { c.Admin.OIDC.OperatorSubjects = list(v); return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_VIEWER_SUBJECTS", "admin.oidc.viewer_subjects", "read-only users by subject_claim, comma-separated (license: sso_rules)", func(c *Config, v string) error { c.Admin.OIDC.ViewerSubjects = list(v); return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_SUBJECT_CLAIM", "admin.oidc.subject_claim", "claim the *_subjects rules match, default sub (Entra ID: oid)", func(c *Config, v string) error { c.Admin.OIDC.SubjectClaim = v; return nil }},
	{"CODEREVIEW_CONSOLE_OIDC_REQUIRE_EMAIL_VERIFIED", "admin.oidc.require_email_verified", "true or false, default true: email and domain rules need email_verified: true", func(c *Config, v string) error {
		var b bool
		if err := setBool(&b, v); err != nil {
			return err
		}
		c.Admin.OIDC.RequireEmailVerified = &b
		return nil
	}},
	{"CODEREVIEW_CONSOLE_OIDC_GROUPS_CLAIM", "admin.oidc.groups_claim", "ID token claim with group names, default groups", func(c *Config, v string) error { c.Admin.OIDC.GroupsClaim = v; return nil }},
	{"CODEREVIEW_CONSOLE_EXECUTOR", "executor.type", "local, docker or kubernetes; default local", func(c *Config, v string) error { c.Executor.Type = v; return nil }},
	{"CODEREVIEW_CONSOLE_CLI_BINARY", "executor.local.binary", "codereview binary for the local executor", func(c *Config, v string) error { c.Executor.Local.Binary = v; return nil }},
	{"CODEREVIEW_CONSOLE_IMAGE", "executor.docker.image, executor.kubernetes.image", "review image (repository, and the tag used until one is saved in the panel)", func(c *Config, v string) error {
		c.Executor.Docker.Image, c.Executor.Kubernetes.Image = v, v
		return nil
	}},
	{"CODEREVIEW_CONSOLE_DOCKER_BINARY", "executor.docker.binary", "docker CLI, default docker", func(c *Config, v string) error { c.Executor.Docker.Binary = v; return nil }},
	{"CODEREVIEW_CONSOLE_DOCKER_NETWORK", "executor.docker.network", "network for review containers", func(c *Config, v string) error { c.Executor.Docker.Network = v; return nil }},
	{"CODEREVIEW_CONSOLE_KUBE_NAMESPACE", "executor.kubernetes.namespace", "namespace for review Jobs, default default", func(c *Config, v string) error { c.Executor.Kubernetes.Namespace = v; return nil }},
	{"CODEREVIEW_CONSOLE_KUBE_SERVICE_ACCOUNT", "executor.kubernetes.service_account", "service account of review pods (cloud identity)", func(c *Config, v string) error { c.Executor.Kubernetes.ServiceAccount = v; return nil }},
	{"CODEREVIEW_CONSOLE_KUBE_AUTOMOUNT_TOKEN", "executor.kubernetes.automount_service_account_token", "true only if review pods need the Kubernetes API token", func(c *Config, v string) error {
		return setBool(&c.Executor.Kubernetes.AutomountServiceAccountToken, v)
	}},
	{"CODEREVIEW_CONSOLE_KUBECONFIG", "executor.kubernetes.kubeconfig", "kubeconfig path; empty = in-cluster", func(c *Config, v string) error { c.Executor.Kubernetes.Kubeconfig = v; return nil }},
}

// applyStartupEnv sets the startup settings found in the environment. It
// returns the names it used.
func (c *Config) applyStartupEnv() ([]string, error) {
	var used []string
	for _, e := range EnvVars {
		v, ok := os.LookupEnv(e.Name)
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		if err := e.set(c, strings.TrimSpace(v)); err != nil {
			return used, fmt.Errorf("%s: %w", e.Name, err)
		}
		used = append(used, e.Name)
	}
	return used, nil
}

func setBool(dst *bool, v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("want true or false, got %q", v)
	}
	*dst = b
	return nil
}

func setDuration(dst *time.Duration, v string) error {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fmt.Errorf("want a duration such as 12h, got %q", v)
	}
	*dst = d
	return nil
}

func list(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
