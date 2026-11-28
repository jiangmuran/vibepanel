package httpapi

import (
	"strings"
	"testing"
)

func TestExtractShareHeadMeta(t *testing.T) {
	cases := []struct {
		name      string
		html      string
		wantTitle string
		wantIcon  string
	}{
		{
			name: "standard title and svg data uri icon",
			html: `<!doctype html><html><head>
				<meta charset="utf-8">
				<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg'%3E%3C/svg%3E" />
				<title>Same Price, Better Model (v8) &mdash; 总览</title>
			</head><body><title>Body Title</title></body></html>`,
			wantTitle: "Same Price, Better Model (v8) — 总览",
			wantIcon:  "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg'%3E%3C/svg%3E",
		},
		{
			name: "skips comments script style and placeholder data icon",
			html: `<!doctype html><head>
				<!-- <title>Commented</title><link rel="icon" href="data:image/png;base64,AAA"> -->
				<script>var x = "<title>Fake</title><link rel='icon' href='./fake.png'>";</script>
				<style>/* <title>CSS</title> */</style>
				<link rel="icon" href="data:,">
				<link rel="shortcut icon" href="./assets/logo.svg?v=2">
				<title>
					  Multi
					  Line   Title  
				</title>
			</head><body><h1>hi</h1></body>`,
			wantTitle: "Multi Line Title",
			wantIcon:  "./assets/logo.svg?v=2",
		},
		{
			name: "stops at body so body tags are never read",
			html: `<!doctype html><body><title>In Body</title><link rel="icon" href="./b.png"></body>`,
		},
		{
			name:      "handles title attributes and bidi stripping",
			html:      `<head><title lang="zh">Hello ` + "\u202e" + `World` + "\x07" + `</title></head>`,
			wantTitle: "Hello World",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTitle, gotIcon := extractShareHeadMeta([]byte(tc.html))
			if gotTitle != tc.wantTitle {
				t.Errorf("title = %q, want %q", gotTitle, tc.wantTitle)
			}
			if gotIcon != tc.wantIcon {
				t.Errorf("icon = %q, want %q", gotIcon, tc.wantIcon)
			}
		})
	}
}

func TestResolveShellIconHref(t *testing.T) {
	grant := "GrantTokenExample1234567890"
	isPrivate := func(p string) bool { return p == "server.js" || strings.HasPrefix(p, "admin/") }

	allowed := []struct {
		raw  string
		want string
	}{
		{
			raw:  "data:image/svg+xml,%3Csvg/%3E",
			want: "data:image/svg+xml,%3Csvg/%3E",
		},
		{
			raw:  "data:image/webp;base64,UklGRg==",
			want: "data:image/webp;base64,UklGRg==",
		},
		{
			raw:  "./favicon.svg?v=3",
			want: "/share-u/" + grant + "/favicon.svg",
		},
		{
			raw:  "assets/app-icon.png#top",
			want: "/share-u/" + grant + "/assets/app-icon.png",
		},
	}
	for _, tc := range allowed {
		if got := resolveShellIconHref(tc.raw, grant, isPrivate); got != tc.want {
			t.Errorf("resolveShellIconHref(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	rejected := []string{
		"",
		"data:,",
		"data:image/svg+xml,",
		"data:text/html,<script>alert(1)</script>",
		"javascript:alert(1)",
		"https://evil.example/icon.png",
		"http://evil.example/icon.png",
		"//evil.example/icon.png",
		"/icon.svg",
		"../secret.png",
		"assets/app icon.png",
		"admin/icon.png",
		"vibepanel.js",
		"data:image/png;base64,abc\ndef",
	}
	for _, raw := range rejected {
		if got := resolveShellIconHref(raw, grant, isPrivate); got != "" {
			t.Errorf("resolveShellIconHref(%q) = %q, want empty", raw, got)
		}
	}
}
