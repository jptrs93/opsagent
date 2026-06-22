package localtest

import "testing"

func TestRewriteGithubRepoURL(t *testing.T) {
	t.Setenv("OPENDEPLOY_LOCAL_TEST", "true")

	tests := map[string]string{
		"github.com/jptrs93/opsagent":             BaseURL + "/jptrs93/opsagent.git",
		"github.com/jptrs93/opsagent.git":         BaseURL + "/jptrs93/opsagent.git",
		"https://github.com/jptrs93/opsagent":     BaseURL + "/jptrs93/opsagent.git",
		"https://github.com/jptrs93/opsagent.git": BaseURL + "/jptrs93/opsagent.git",
		"git@github.com:jptrs93/opsagent.git":     BaseURL + "/jptrs93/opsagent.git",
	}
	for input, want := range tests {
		got, ok := RewriteGithubRepoURL(input)
		if !ok || got != want {
			t.Fatalf("RewriteGithubRepoURL(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
}

func TestRewriteGithubRepoURLDisabled(t *testing.T) {
	t.Setenv("OPENDEPLOY_LOCAL_TEST", "false")

	input := "github.com/jptrs93/opsagent"
	got, ok := RewriteGithubRepoURL(input)
	if ok || got != input {
		t.Fatalf("RewriteGithubRepoURL disabled = %q, %v; want %q, false", got, ok, input)
	}
}
