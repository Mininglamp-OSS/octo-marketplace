package service

import (
	"github.com/Mininglamp-OSS/octo-marketplace/internal/apierr"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

// redactSecrets normalizes an env/header map on write. The prior write-time
// guardrails (must_be_empty on user-supplied keys, public_secret_disallowed on
// secret-shaped shared keys) have both been removed — values are now persisted
// verbatim on any visibility. The single defense line is detailForCaller
// (§5.3): non-owner reads blank every value in `config.env` / `config.headers`
// so consumers never see the author's persisted tokens through the API. The
// consumer-facing snippet on the client further substitutes the shared
// placeholder for any `*_user_supplied` key.
//
// SecretPlaceholderSentinel is still accepted from legacy clients and
// normalized to "" so the DB doesn't accumulate the placeholder literal.
//
// The `apierr.Detail` return remains for signature stability with older
// callers, but is always nil — no reason path in this function rejects.
// `field` / `userSupplied` / `visibility` parameters are similarly retained
// for compat with the surrounding call sites.
func redactSecrets(
	in map[string]string,
	field string,
	userSupplied []string,
	visibility model.Visibility,
) (map[string]string, []apierr.Detail) {
	_ = field
	_ = userSupplied
	_ = visibility
	if len(in) == 0 {
		return in, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if v == model.SecretPlaceholderSentinel {
			out[k] = ""
		} else {
			out[k] = v
		}
	}
	return out, nil
}
