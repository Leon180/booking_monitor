package domain

import "strings"

// NormalizeCurrency lowercases a currency code so storage + downstream
// gateway calls see one canonical form. Stripe's API requires
// lowercase ISO 4217 codes ("usd", not "USD"); KKTIX exposes the same
// convention. Normalising at the domain factory means the rest of the
// system never has to think about case.
//
// Returns the normalised value AND the original `len()` so callers
// can validate `len == 3` separately. We intentionally do NOT trim
// surrounding whitespace — a 4-byte input like " usd" is a malformed
// payload, not a forgivable typo.
func NormalizeCurrency(s string) string {
	return strings.ToLower(s)
}

// isValidCurrencyCode reports whether s looks like a 3-letter ISO 4217
// code. We don't check the full ISO 4217 list — the standard has
// ~180 entries and changes over time (currency-board reorganisations,
// new sovereign currencies). Application-layer validation against a
// curated allowlist belongs at the API DTO boundary, not the domain.
// The domain enforces only the *shape* invariant: 3 ASCII letters.
// The downstream gateway (Stripe) does the real ISO 4217 enforcement.
//
// Callers should pass the value through `NormalizeCurrency` first so
// case normalisation and shape validation are decoupled.
func isValidCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}
