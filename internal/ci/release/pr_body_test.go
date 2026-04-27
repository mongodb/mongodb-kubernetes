package release

import (
	"strings"
	"testing"
)

func TestPRTitle(t *testing.T) {
	got := PRTitle("1.8.0")
	want := "Release MCK 1.8.0"
	if got != want {
		t.Errorf("PRTitle: got %q, want %q", got, want)
	}
}

func TestRenderPRBody_ContainsKeyMarkers(t *testing.T) {
	body, err := RenderPRBody("1.8.0")
	if err != nil {
		t.Fatalf("RenderPRBody: %v", err)
	}
	for _, want := range []string{
		"Release PR for MCK 1.8.0.",
		"Bump `mongodbOperator` to 1.8.0",
		"public/dockerfiles/*/1.8.0/ubi/",
		"`release.json` `mongodbOperator` = 1.8.0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered body missing %q\n--body--\n%s", want, body)
		}
	}
}

func TestRenderPRBody_NoUnrenderedPlaceholders(t *testing.T) {
	body, err := RenderPRBody("1.8.0")
	if err != nil {
		t.Fatalf("RenderPRBody: %v", err)
	}
	if strings.Contains(body, "{{") || strings.Contains(body, "}}") {
		t.Errorf("body contains unrendered template markers:\n%s", body)
	}
}
