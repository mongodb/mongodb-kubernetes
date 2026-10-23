package ommetrics

import (
	"fmt"
	"net/url"
	"strings"
)

type Project struct {
	// BaseURL is canonicalized, so any spelling of the same OM works.
	BaseURL string
	GroupID string
}

type destKey struct {
	base    string
	groupID string
}

func (k destKey) String() string { return k.base + "|" + k.groupID }

func (k destKey) metricsURL() string {
	return k.base + "/agents/api/otlp/" + k.groupID + "/v1/metrics"
}

func (p Project) key() (destKey, error) {
	// The group ID is spliced into the URL path unescaped.
	if p.GroupID == "" || url.PathEscape(p.GroupID) != p.GroupID {
		return destKey{}, fmt.Errorf("ommetrics: invalid group ID %q", p.GroupID)
	}
	base, err := canonicalBase(p.BaseURL)
	if err != nil {
		return destKey{}, err
	}
	return destKey{base: base, groupID: p.GroupID}, nil
}

// canonicalBase normalizes an OM base URL so CRs pointing at the same OM share a destination key.
func canonicalBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("ommetrics: unusable OM base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("ommetrics: OM base URL %q needs a scheme and host", u.Redacted())
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = u.Hostname()
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String(), nil
}
