package github

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCredentialHelperTokenSourceParsesPasswordAndCaches(t *testing.T) {
	calls := 0
	src := &CredentialHelperTokenSource{
		Fill: func(ctx context.Context, host string) (string, error) {
			calls++
			if host != defaultCredentialHost {
				t.Fatalf("host = %q, want %q", host, defaultCredentialHost)
			}
			return "ghp_fromhelper", nil
		},
		TokenTTL: time.Hour,
	}
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "ghp_fromhelper" {
		t.Fatalf("Token = %q, want %q", tok, "ghp_fromhelper")
	}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if calls != 1 {
		t.Fatalf("Fill called %d times; want 1 (cache hit)", calls)
	}
	src.InvalidateToken()
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("third Token: %v", err)
	}
	if calls != 2 {
		t.Fatalf("after invalidate, Fill called %d times; want 2", calls)
	}
}

func TestCredentialHelperTokenSourceEmptyIsErrNoToken(t *testing.T) {
	src := &CredentialHelperTokenSource{
		Fill: func(ctx context.Context, host string) (string, error) { return "   ", nil },
	}
	if _, err := src.Token(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestCredentialHelperTokenSourceHonorsHost(t *testing.T) {
	src := &CredentialHelperTokenSource{
		Host: "ghe.example.com",
		Fill: func(ctx context.Context, host string) (string, error) {
			if host != "ghe.example.com" {
				t.Fatalf("host = %q, want ghe.example.com", host)
			}
			return "t", nil
		},
	}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
}

func TestParseCredentialPassword(t *testing.T) {
	cases := map[string]string{
		"protocol=https\nhost=github.com\nusername=x\npassword=secret\n": "secret",
		"password=only\n": "only",
		"username=x\n":    "",
		"":                "",
	}
	for in, want := range cases {
		if got := parseCredentialPassword(in); got != want {
			t.Fatalf("parseCredentialPassword(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBestEffortLoginPrefersSSHThenEmail(t *testing.T) {
	r := &BestEffortLoginResolver{
		SSH:   func(ctx context.Context) (string, error) { return "ssh-user", nil },
		Email: func(ctx context.Context) (string, error) { return "email-user", nil },
		TTL:   time.Hour,
	}
	login, err := r.BestEffortLogin(context.Background())
	if err != nil {
		t.Fatalf("BestEffortLogin: %v", err)
	}
	if login != "ssh-user" {
		t.Fatalf("login = %q, want ssh-user", login)
	}
}

func TestBestEffortLoginFallsBackToEmail(t *testing.T) {
	emailCalls := 0
	r := &BestEffortLoginResolver{
		SSH:   func(ctx context.Context) (string, error) { return "", errNoBestEffortLogin },
		Email: func(ctx context.Context) (string, error) { emailCalls++; return "email-user", nil },
		TTL:   time.Hour,
	}
	login, _ := r.BestEffortLogin(context.Background())
	if login != "email-user" {
		t.Fatalf("login = %q, want email-user", login)
	}
	// Second call is served from cache: neither probe re-runs.
	if _, err := r.BestEffortLogin(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}
	if emailCalls != 1 {
		t.Fatalf("Email called %d times; want 1 (cached)", emailCalls)
	}
}

func TestBestEffortLoginEmptyWhenNoSignal(t *testing.T) {
	sshCalls := 0
	r := &BestEffortLoginResolver{
		SSH:   func(ctx context.Context) (string, error) { sshCalls++; return "", errNoBestEffortLogin },
		Email: func(ctx context.Context) (string, error) { return "", errNoBestEffortLogin },
		TTL:   time.Hour,
	}
	login, err := r.BestEffortLogin(context.Background())
	if err != nil {
		t.Fatalf("BestEffortLogin returned err %v; want nil (degrade to empty)", err)
	}
	if login != "" {
		t.Fatalf("login = %q, want empty", login)
	}
	// The empty result is cached too, so a machine with no signal is not
	// re-probed on every spawn.
	if _, err := r.BestEffortLogin(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}
	if sshCalls != 1 {
		t.Fatalf("SSH called %d times; want 1 (empty result cached)", sshCalls)
	}
}

func TestSSHGreetingRegex(t *testing.T) {
	greeting := "Warning: Permanently added 'github.com' to the list of known hosts.\n" +
		"Hi Pulkit7070! You've successfully authenticated, but GitHub does not provide shell access.\n"
	m := sshGreetingRe.FindStringSubmatch(greeting)
	if len(m) < 2 || m[1] != "Pulkit7070" {
		t.Fatalf("match = %v, want Pulkit7070", m)
	}
	if sshGreetingRe.MatchString("Permission denied (publickey).") {
		t.Fatalf("should not match a denied greeting")
	}
}

func TestNoreplyRegex(t *testing.T) {
	cases := map[string]string{
		"146842937+Pulkit7070@users.noreply.github.com": "Pulkit7070",
		"octocat@users.noreply.github.com":              "octocat",
		"146842937+Pulkit7070@USERS.NOREPLY.GITHUB.COM": "Pulkit7070",
		"prateek.saraf@gmail.com":                       "",
		"someone@example.com":                           "",
	}
	for email, want := range cases {
		m := noreplyRe.FindStringSubmatch(email)
		got := ""
		if len(m) >= 2 {
			got = m[1]
		}
		if got != want {
			t.Fatalf("noreplyRe(%q) = %q, want %q", email, got, want)
		}
	}
}
