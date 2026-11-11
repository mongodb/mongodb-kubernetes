package ommetrics

import "testing"

func TestProjectKeyCanonicalization(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		want    string
		wantErr bool
	}{
		{name: "plain", base: "https://om.example.com", want: "https://om.example.com|g1"},
		{name: "default port", base: "https://om.example.com:443", want: "https://om.example.com|g1"},
		{name: "trailing slash", base: "https://om.example.com/", want: "https://om.example.com|g1"},
		{name: "http default port", base: "http://om.example.com:80", want: "http://om.example.com|g1"},
		{name: "non-default port kept", base: "https://om.example.com:8443", want: "https://om.example.com:8443|g1"},
		{name: "path kept", base: "https://om.example.com/om", want: "https://om.example.com/om|g1"},
		{name: "query and fragment stripped", base: "https://om.example.com/?x=1#f", want: "https://om.example.com|g1"},
		{name: "uppercase host and scheme", base: "HTTPS://OM.Example.Com", want: "https://om.example.com|g1"},
		{name: "missing scheme", base: "om.example.com", wantErr: true},
		{name: "missing host", base: "https://", wantErr: true},
		{name: "garbage", base: "://", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Project{BaseURL: tc.base, GroupID: "g1"}.key()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("key(%q) expected error, got %q", tc.base, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("key(%q) unexpected error: %v", tc.base, err)
			}
			if got.String() != tc.want {
				t.Fatalf("key(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

func TestProjectKeyRejectsUnsafeGroupID(t *testing.T) {
	for _, g := range []string{"", "a/b", "../x", "a?b", "a%2Fb", "a b"} {
		if _, err := (Project{BaseURL: "https://om.example.com", GroupID: g}).key(); err == nil {
			t.Errorf("key accepted group ID %q", g)
		}
	}
	if _, err := (Project{BaseURL: "https://om.example.com", GroupID: "5f1e2d3c4b5a69788796a5b4"}).key(); err != nil {
		t.Fatalf("key rejected an ObjectID group ID: %v", err)
	}
}

// The three spellings of the same OM must collapse to one destination.
func TestProjectKeyEqualAcrossSpellings(t *testing.T) {
	var keys []destKey
	for _, base := range []string{"https://om.example.com", "https://om.example.com:443", "https://om.example.com/"} {
		k, err := Project{BaseURL: base, GroupID: "g1"}.key()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if keys[0] != keys[1] || keys[1] != keys[2] {
		t.Fatalf("canonicalization failed: %v", keys)
	}
}

func TestMetricsURL(t *testing.T) {
	k := destKey{base: "https://om.example.com/om", groupID: "g1"}
	if got, want := k.metricsURL(), "https://om.example.com/om/agents/api/otlp/g1/v1/metrics"; got != want {
		t.Fatalf("metricsURL() = %q, want %q", got, want)
	}
}

func TestProjectFromLink(t *testing.T) {
	tests := []struct {
		name   string
		link   string
		want   Project
		wantOK bool
	}{
		{name: "status link", link: "https://om.example.com:8443/v2/g1", want: Project{BaseURL: "https://om.example.com:8443", GroupID: "g1"}, wantOK: true},
		{name: "base path containing /v2/", link: "https://om.example.com/v2/proxy/v2/g1", want: Project{BaseURL: "https://om.example.com/v2/proxy", GroupID: "g1"}, wantOK: true},
		{name: "empty", link: ""},
		{name: "no /v2/", link: "https://om.example.com/g1"},
		{name: "empty group", link: "https://om.example.com/v2/"},
		{name: "unsafe group", link: "https://om.example.com/v2/g1/extra"},
		{name: "no base", link: "/v2/g1"},
		{name: "relative base", link: "om.example.com/v2/g1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ProjectFromLink(tc.link)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("ProjectFromLink(%q) = %+v, %v; want %+v, %v", tc.link, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestProjectSameDestination(t *testing.T) {
	a := Project{BaseURL: "https://om.example.com", GroupID: "g1"}
	tests := []struct {
		name  string
		other Project
		want  bool
	}{
		{name: "identical", other: a, want: true},
		{name: "different spelling", other: Project{BaseURL: "HTTPS://OM.example.com:443/", GroupID: "g1"}, want: true},
		{name: "different group", other: Project{BaseURL: "https://om.example.com", GroupID: "g2"}},
		{name: "different OM", other: Project{BaseURL: "https://om2.example.com", GroupID: "g1"}},
		{name: "invalid other", other: Project{BaseURL: "https://om.example.com", GroupID: ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.SameDestination(tc.other); got != tc.want {
				t.Fatalf("SameDestination(%+v) = %v, want %v", tc.other, got, tc.want)
			}
		})
	}
}
