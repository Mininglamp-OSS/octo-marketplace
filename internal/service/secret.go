package service

import (
	"regexp"
	"strings"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// secretKeyPattern matches token-like keys (doc §5.1). It intentionally covers
// common password/session/auth credential names so write-time redaction is not
// bypassed by alternate key names.
var secretKeyPattern = regexp.MustCompile(`(?i)^(authorization|.*authorization|token|.*token|.*key|.*secret|password|.*password|pwd|.*pwd|passwd|pass|passphrase|api[-_]?key|pat|cookie|.*cookie|credential|credentials|.*credential|auth|.*auth|bearer|.*bearer|session|.*session|sessionid)$`)

// isSecretKey reports whether k names a token-like field.
func isSecretKey(k string) bool {
	return secretKeyPattern.MatchString(strings.TrimSpace(k))
}

// redactSecrets applies the write-time redaction rules from doc §5 to an
// env/header map, in place-free fashion, returning the sanitized copy. field
// prefixes the key in any error detail (e.g. "env" or "headers").
//
// For each token-like key:
//   - empty OR the shared sentinel  -> accepted, stored as empty string
//   - any other non-empty value     -> whole request rejected (secret_leaked)
//
// Non-matching keys pass through verbatim. The Authorization header is always
// forced to empty even though the pattern already covers it, matching the doc's
// "config.headers.Authorization is stripped on write and never returned".
func redactSecrets(in map[string]string, field string) (map[string]string, []apierr.Detail) {
	if len(in) == 0 {
		return in, nil
	}
	out := make(map[string]string, len(in))
	var leaks []apierr.Detail
	for k, v := range in {
		if !isSecretKey(k) {
			out[k] = v
			continue
		}
		if v == "" || v == model.SecretPlaceholderSentinel {
			out[k] = ""
			continue
		}
		leaks = append(leaks, apierr.Detail{Field: field + "." + k, Reason: "non_empty"})
	}
	return out, leaks
}
