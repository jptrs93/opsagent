package preparer

import (
	"context"
	"testing"

	"github.com/jptrs93/opsagent/backend/engine/credentials"
)

type testGithubCredentialsProvider struct {
	token string
}

func (p testGithubCredentialsProvider) GithubCredentials(context.Context) (credentials.GithubCredentials, error) {
	return credentials.GithubCredentials{Token: p.token}, nil
}

func TestGitManagerResolveCloneURLNormalizesGithubRepo(t *testing.T) {
	tests := []struct {
		name string
		repo string
		want string
	}{
		{
			name: "bare github host path",
			repo: "github.com/acme/widget",
			want: "https://x-access-token:secret@github.com/acme/widget.git",
		},
		{
			name: "https url",
			repo: "https://github.com/acme/widget",
			want: "https://x-access-token:secret@github.com/acme/widget.git",
		},
		{
			name: "https url with git suffix",
			repo: "https://github.com/acme/widget.git",
			want: "https://x-access-token:secret@github.com/acme/widget.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewGitManager("", testGithubCredentialsProvider{token: "secret"}).resolveCloneURL(context.Background(), tt.repo)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("resolveCloneURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
