package domain

import (
	"math"
	"regexp"
	"strings"
)

// DetectorKind separates detectors that can't be relaxed (format) from those owners may set to
// warn or allow-list (heuristic), spec §9.1.
type DetectorKind string

// Detector kinds.
const (
	DetectorFormat    DetectorKind = "format"
	DetectorHeuristic DetectorKind = "heuristic"
)

// Detector is one secret detector of spec Appendix B.
type Detector struct {
	Name    string
	Kind    DetectorKind
	Pattern *regexp.Regexp
	// accept, when set, decides whether a regexp match really is a secret.
	accept func(match []string) bool
}

const secretKeywords = `(?:password|passwd|secret|token|api[_-]?key|client[_-]?secret)`

var placeholder = regexp.MustCompile(`(?i)^(?:<.*>|\$\{.*\}|\*+|x+|changeme|example.*|your[_-].*|dummy.*|placeholder.*|redacted)$`)

// Detectors is the minimum set of spec Appendix B.
var Detectors = []Detector{
	{Name: "private_key", Kind: DetectorFormat, Pattern: regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY(?: BLOCK)?-----`)},
	{Name: "aws_access_key_id", Kind: DetectorFormat, Pattern: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{Name: "aws_secret_access_key", Kind: DetectorFormat, Pattern: regexp.MustCompile(`(?i)(?:aws_secret_access_key|secretAccessKey)\s*[:=]\s*["']?[A-Za-z0-9/+=]{40}`)},
	{Name: "github_token", Kind: DetectorFormat, Pattern: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,255}\b|\bgithub_pat_[A-Za-z0-9_]{82}\b`)},
	{Name: "gitlab_token", Kind: DetectorFormat, Pattern: regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{Name: "slack_token", Kind: DetectorFormat, Pattern: regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`)},
	{Name: "stripe_key", Kind: DetectorFormat, Pattern: regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}\b`)},
	{Name: "google_api_key", Kind: DetectorFormat, Pattern: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{Name: "jwt", Kind: DetectorFormat, Pattern: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{Name: "url_credentials", Kind: DetectorFormat, Pattern: regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`)},
	{
		Name: "assigned_secret", Kind: DetectorHeuristic,
		Pattern: regexp.MustCompile(`(?i)` + secretKeywords + `\s*[:=]\s*["']?([^\s"']{8,})`),
		accept:  func(m []string) bool { return !placeholder.MatchString(m[1]) },
	},
	{
		Name: "high_entropy", Kind: DetectorHeuristic,
		Pattern: regexp.MustCompile(`(?i)` + secretKeywords + `[^\n]{0,40}?([A-Za-z0-9+/_=-]{32,})`),
		accept:  func(m []string) bool { return highEntropy(m[1]) },
	},
}

// Finding is a detector match in one field. It never carries the matched text, so it can be
// logged and returned safely.
type Finding struct {
	Field    string
	Detector string
	Kind     DetectorKind
}

// ScanPolicy is a workspace's secret_detectors setting: per heuristic detector "reject" (default)
// or "warn", and allow-list patterns that exempt heuristic matches.
type ScanPolicy struct {
	Modes     map[string]string
	AllowList []*regexp.Regexp
}

// Scan returns the reject and warn findings for the given fields (name -> text).
func Scan(fields map[string]string, p ScanPolicy) (reject, warn []Finding) {
	for field, text := range fields {
		for _, d := range Detectors {
			for _, m := range d.Pattern.FindAllStringSubmatch(text, -1) {
				if d.accept != nil && !d.accept(m) {
					continue
				}
				f := Finding{Field: field, Detector: d.Name, Kind: d.Kind}
				if d.Kind == DetectorHeuristic {
					if allowed(m[0], p.AllowList) {
						continue
					}
					if p.Modes[d.Name] == "warn" {
						warn = append(warn, f)
						break
					}
				}
				reject = append(reject, f)
				break
			}
		}
	}
	return reject, warn
}

func allowed(match string, list []*regexp.Regexp) bool {
	for _, re := range list {
		if re.MatchString(match) {
			return true
		}
	}
	return false
}

// highEntropy applies Appendix B's thresholds: 3.0 bits per character for hex, 4.0 otherwise.
func highEntropy(s string) bool {
	limit := 4.0
	if strings.Trim(strings.ToLower(s), "0123456789abcdef") == "" {
		limit = 3.0
	}
	counts := map[rune]float64{}
	for _, r := range s {
		counts[r]++
	}
	var h float64
	n := float64(len([]rune(s)))
	for _, c := range counts {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h > limit
}
