package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestParseInterspersedFlagsAfterPositionals(t *testing.T) {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	top := fs.Int("top", 0, "")
	asJSON := fs.Bool("json", false, "")
	pos, err := parseInterspersed(fs,
		[]string{"登录失败处理", "--top", "3", "--json", "extra"},
		map[string]bool{"top": true})
	if err != nil {
		t.Fatal(err)
	}
	if *top != 3 || !*asJSON {
		t.Fatalf("top=%d json=%v", *top, *asJSON)
	}
	if !reflect.DeepEqual(pos, []string{"登录失败处理", "extra"}) {
		t.Fatalf("positionals = %v", pos)
	}
}

func TestParseInterspersedEqualsForm(t *testing.T) {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	top := fs.Int("top", 0, "")
	pos, err := parseInterspersed(fs, []string{"--top=5", "query"}, map[string]bool{"top": true})
	if err != nil {
		t.Fatal(err)
	}
	if *top != 5 || len(pos) != 1 || pos[0] != "query" {
		t.Fatalf("top=%d pos=%v", *top, pos)
	}
}

func TestParseInterspersedMissingValue(t *testing.T) {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.Int("top", 0, "")
	if _, err := parseInterspersed(fs, []string{"query", "--top"}, map[string]bool{"top": true}); err == nil {
		t.Fatal("want error for missing flag value")
	}
}
