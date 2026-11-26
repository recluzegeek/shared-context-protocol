package domain

import (
	"strings"
	"unicode"
)

// Tokenize splits text into search tokens as spec §5.4.3 defines: at every character that is not
// a Unicode letter or digit, lowercased.
func Tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// MatchesQuery reports whether every query token is a prefix of at least one text token: the
// minimum every server must return (spec §5.4.3).
func MatchesQuery(queryTokens, textTokens []string) bool {
	for _, q := range queryTokens {
		found := false
		for _, t := range textTokens {
			if strings.HasPrefix(t, q) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
