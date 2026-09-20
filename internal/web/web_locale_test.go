package web

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestWebIndexHonorsConfiguredDefaultLocale(t *testing.T) {
	body, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, needle := range []string{
		"window.M365_DEFAULT_LOCALE",
	} {
		if !strings.Contains(page, needle) {
			t.Fatalf("web index missing default locale hook %q", needle)
		}
	}
}

func TestDefaultLocaleSnippetWhitelist(t *testing.T) {
	cases := []struct {
		env  string
		want string // empty means no snippet
	}{
		{"", ""},
		{"   ", ""},
		{"zh-CN", ""},
		{"ko", `window.M365_DEFAULT_LOCALE="ko";`},
		{"en", `window.M365_DEFAULT_LOCALE="en";`},
		{"pt-BR", `window.M365_DEFAULT_LOCALE="pt-BR";`},
		{"korean", ""},
		{"<script>alert(1)</script>", ""},
		{"';alert(1);//", ""},
	}
	for _, c := range cases {
		t.Setenv("M365_DEFAULT_LOCALE", c.env)
		got := defaultLocaleSnippet()
		if c.want == "" {
			if got != "" {
				t.Fatalf("env %q: expected no snippet, got %q", c.env, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Fatalf("env %q: snippet %q missing %q", c.env, got, c.want)
		}
	}
}

func TestPageWithDefaultLocaleInjectsAfterHead(t *testing.T) {
	page := []byte("<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\"><title>x</title></head><body></body></html>")
	t.Setenv("M365_DEFAULT_LOCALE", "ko")
	out := string(pageWithDefaultLocale(page))
	if !strings.Contains(out, "<head><script>window.M365_DEFAULT_LOCALE=\"ko\";</script><meta charset=\"utf-8\">") {
		t.Fatalf("injection misplaced: %s", out)
	}
	// zh-CN (default) must leave the page untouched.
	t.Setenv("M365_DEFAULT_LOCALE", "zh-CN")
	if string(pageWithDefaultLocale(page)) != string(page) {
		t.Fatal("zh-CN default must not modify the page")
	}
	// Invalid value must leave the page untouched.
	t.Setenv("M365_DEFAULT_LOCALE", "xx-XX")
	if string(pageWithDefaultLocale(page)) != string(page) {
		t.Fatal("invalid locale must not modify the page")
	}
}

func TestWebIndexDefaultsToChineseUntilLocaleIsSelected(t *testing.T) {
	body, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, needle := range []string{
		"const localeSelectionKey='m365_locale_selected';",
		"function preferredLocale()",
		"return 'zh-CN';",
		"localStorage.setItem(localeSelectionKey,'1')",
	} {
		if !strings.Contains(page, needle) {
			t.Fatalf("web index missing Chinese default bootstrap %q", needle)
		}
	}
}

func TestWebIndexIncludesAccountMonitoringControls(t *testing.T) {
	body, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, needle := range []string{
		`data-f="cooldown"`,
		`x.status==='cooldown'`,
		`/api/accounts/schedule`,
		`id="accountSelectAll"`,
		`function selectedAccountIds()`,
		`setSelectedAccountScheduling(true)`,
		`setSelectedAccountScheduling(false)`,
		`x.callCount||0`,
		`x.rateLimited`,
		`Limited after ${x.callCount||0} calls`,
	} {
		if !strings.Contains(page, needle) {
			t.Fatalf("web index missing cooldown control %q", needle)
		}
	}
}

func TestWebIndexIncludesExperimentalImageSetting(t *testing.T) {
	body, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, needle := range []string{
		`id="setImageApi"`,
		`v.enableImageApi===true`,
		`body.enableImageApi=$('setImageApi').checked`,
		`Disabled by default. Enable only after independently verifying the upstream image protocol and credentials.`,
	} {
		if !strings.Contains(page, needle) {
			t.Fatalf("web index missing image API setting %q", needle)
		}
	}
}

func TestWebIndexKeepsGraphBatchCreationDormant(t *testing.T) {
	body, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, needle := range []string{
		`<template id="disabledGraphBatchUserCreation">`,
		`const disabledGraphBatchUserCreationImplementation=()=>{`,
		`/api/admin/graph/authorization/status`,
		`/api/admin/graph/users/batch`,
	} {
		if !strings.Contains(page, needle) {
			t.Fatalf("web index missing dormant Graph implementation %q", needle)
		}
	}
	if strings.Contains(page, "disabledGraphBatchUserCreationImplementation();") {
		t.Fatal("web index activates disabled Graph batch user creation")
	}
}

func TestWebIndexesContainNoReplacementCharactersAndMatch(t *testing.T) {
	primary, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(primary, []byte("\xef\xbf\xbd")) {
		t.Fatal("web/index.html contains U+FFFD")
	}
	if bytes.Contains(embedded, []byte("\xef\xbf\xbd")) {
		t.Fatal("internal/web/web/index.html contains U+FFFD")
	}
	if !bytes.Equal(primary, embedded) {
		t.Fatal("web index files are not byte-identical")
	}
}
