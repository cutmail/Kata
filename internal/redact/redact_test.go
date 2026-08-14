package redact

import "testing"

// TestURLRemovesCredentials は、URL に埋め込まれた資格情報が落ちることを確かめる。
// kata.lock はコミットされる前提なので、ここを通さないとトークンが公開される。
func TestURLRemovesCredentials(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"https://x-access-token:ghp_SECRET@github.com/acme/skills",
			"https://redacted@github.com/acme/skills",
		},
		{
			"https://user@example.com/repo.git",
			"https://redacted@example.com/repo.git",
		},
		// 資格情報が無ければそのまま。
		{"https://github.com/anthropics/skills", "https://github.com/anthropics/skills"},
		// URL として解析できないものは触らない（そこに秘密は入らない）。
		{"git@github.com:acme/skills.git", "git@github.com:acme/skills.git"},
		{"./local/skills/a", "./local/skills/a"},
	}
	for _, tc := range cases {
		if got := URL(tc.in); got != tc.want {
			t.Fatalf("URL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestURLKeepsNoSecretSubstring は、元のトークン文字列が残らないことを確かめる。
func TestURLKeepsNoSecretSubstring(t *testing.T) {
	const secret = "ghp_SUPERSECRETTOKEN123"
	got := URL("https://x-access-token:" + secret + "@github.com/acme/skills")
	if contains(got, secret) {
		t.Fatalf("URL result %q still contains the token", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
