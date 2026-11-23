package hosts

import (
	"fmt"
	"slices"
)

// SecretChanges names the secrets that differ between old and new, never
// their values, for the audit log: "token:changed", "app_private_key:set",
// "signing_tokens:+1", "webhook_secrets:replaced".
func SecretChanges(old, new Secrets) []string {
	out := []string{}
	out = append(out, valueChange("token", old.Token, new.Token)...)
	out = append(out, valueChange("app_private_key", old.AppPrivateKey, new.AppPrivateKey)...)
	out = append(out, valueChange("api_token", old.APIToken, new.APIToken)...)
	return out
}

// EndpointSecretChanges is SecretChanges for a webhook's secrets.
func EndpointSecretChanges(old, new EndpointSecrets) []string {
	out := []string{}
	out = append(out, listChange("webhook_secrets", old.WebhookSecrets, new.WebhookSecrets)...)
	out = append(out, listChange("signing_tokens", old.SigningTokens, new.SigningTokens)...)
	return out
}

func valueChange(name, old, new string) []string {
	switch {
	case old == new:
		return nil
	case old == "":
		return []string{name + ":set"}
	case new == "":
		return []string{name + ":removed"}
	}
	return []string{name + ":changed"}
}

func listChange(name string, old, new []string) []string {
	var added, removed int
	for _, v := range new {
		if !slices.Contains(old, v) {
			added++
		}
	}
	for _, v := range old {
		if !slices.Contains(new, v) {
			removed++
		}
	}
	switch {
	case added == 0 && removed == 0:
		return nil
	case added == removed:
		return []string{name + ":replaced"}
	}
	var out []string
	if added > 0 {
		out = append(out, fmt.Sprintf("%s:+%d", name, added))
	}
	if removed > 0 {
		out = append(out, fmt.Sprintf("%s:-%d", name, removed))
	}
	return out
}
