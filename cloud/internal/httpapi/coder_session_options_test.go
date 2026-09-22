package httpapi

import "testing"

func TestParseCoderSessionOptions(t *testing.T) {
	t.Parallel()
	const goodTemplate = "2a2e262c-b31c-4202-946d-a19ad45d1fd2"

	t.Run("empty options are inert", func(t *testing.T) {
		opts, repos, err := parseCoderSessionOptions(&createSessionCoderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if opts.TemplateID != "" || opts.Size != "" || opts.StartupScript != "" || repos != nil {
			t.Fatalf("empty options not empty: %+v repos=%+v", opts, repos)
		}
	})

	t.Run("template + size + startup + repo", func(t *testing.T) {
		opts, repos, err := parseCoderSessionOptions(&createSessionCoderOptions{
			TemplateID:    goodTemplate,
			Size:          "Large",
			StartupScript: "make dev",
			ExtraRepos:    []createSessionRepo{{URL: "https://github.com/acme/lib.git", Branch: "main"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if opts.TemplateID != goodTemplate || opts.Size != "large" || opts.StartupScript != "make dev" {
			t.Fatalf("options = %+v", opts)
		}
		if len(repos) != 1 || repos[0].URL != "https://github.com/acme/lib" || repos[0].Branch != "main" {
			t.Fatalf("repos = %+v", repos)
		}
	})

	t.Run("bad template UUID is rejected", func(t *testing.T) {
		if _, _, err := parseCoderSessionOptions(&createSessionCoderOptions{TemplateID: "not-a-uuid"}); err == nil {
			t.Fatal("expected error for bad template UUID")
		}
	})

	t.Run("bad size is rejected", func(t *testing.T) {
		if _, _, err := parseCoderSessionOptions(&createSessionCoderOptions{TemplateID: goodTemplate, Size: "huge"}); err == nil {
			t.Fatal("expected error for bad size")
		}
	})

	t.Run("size without a template is rejected", func(t *testing.T) {
		if _, _, err := parseCoderSessionOptions(&createSessionCoderOptions{Size: "large"}); err == nil {
			t.Fatal("expected error: size requires a chosen template")
		}
	})

	t.Run("non-github extra repo is rejected", func(t *testing.T) {
		if _, _, err := parseCoderSessionOptions(&createSessionCoderOptions{
			ExtraRepos: []createSessionRepo{{URL: "https://gitlab.com/a/b"}},
		}); err == nil {
			t.Fatal("expected error for non-github extra repo")
		}
	})
}
