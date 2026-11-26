package server

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeTranslator upper-cases each string with an "EN:" prefix and records the batches
// it was asked to translate.
type fakeTranslator struct{ calls [][]string }

func (f *fakeTranslator) translate(_ context.Context, src []string) ([]string, error) {
	f.calls = append(f.calls, src)
	out := make([]string, len(src))
	for i, s := range src {
		out[i] = "EN:" + strings.TrimSpace(s)
	}
	return out, nil
}

func TestTranslateParagraphsCachesAndPassesThrough(t *testing.T) {
	store := map[string]string{}
	lookup := func(hs []string) (map[string]string, error) {
		m := map[string]string{}
		for _, h := range hs {
			if v, ok := store[h]; ok {
				m[h] = v
			}
		}
		return m, nil
	}
	save := func(h, v string) error { store[h] = v; return nil }

	text := "你好\r\n\r\nplain English {{.Goal}}\n\n你好\n  \n세계"
	f := &fakeTranslator{}
	got, err := translateParagraphs(context.Background(), text, lookup, save, f.translate)
	if err != nil {
		t.Fatal(err)
	}
	want := "EN:你好\n\nplain English {{.Goal}}\n\nEN:你好\n  \nEN:세계"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if len(f.calls) != 1 || len(f.calls[0]) != 2 {
		t.Fatalf("want one batch of the two unique CJK paragraphs, got %v", f.calls)
	}

	// A second run with one paragraph edited only translates that paragraph.
	f2 := &fakeTranslator{}
	got, err = translateParagraphs(context.Background(), "你好\n\n世界", lookup, save, f2.translate)
	if err != nil {
		t.Fatal(err)
	}
	if got != "EN:你好\n\nEN:世界" {
		t.Fatalf("got %q", got)
	}
	if len(f2.calls) != 1 || len(f2.calls[0]) != 1 || f2.calls[0][0] != "世界" {
		t.Fatalf("want only the new paragraph translated, got %v", f2.calls)
	}
}

func TestTranslateParagraphsSurvivesCacheErrors(t *testing.T) {
	lookup := func([]string) (map[string]string, error) { return nil, errors.New("db down") }
	save := func(string, string) error { return errors.New("db down") }
	f := &fakeTranslator{}
	got, err := translateParagraphs(context.Background(), "你好", lookup, save, f.translate)
	if err != nil || got != "EN:你好" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestTranslateParagraphsBatchesBySize(t *testing.T) {
	big := strings.Repeat("字", translateBatchChars/3) // 3 bytes per rune: one paragraph fills a batch
	text := big + "一\n\n" + big + "二"
	none := func([]string) (map[string]string, error) { return map[string]string{}, nil }
	f := &fakeTranslator{}
	if _, err := translateParagraphs(context.Background(), text, none, func(string, string) error { return nil }, f.translate); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("want 2 batches, got %d", len(f.calls))
	}
}

func TestParseTranslationArray(t *testing.T) {
	if out, ok := parseTranslationArray("```json\n[\"a\", \"b\"]\n```", 2); !ok || out[1] != "b" {
		t.Errorf("fenced array: %v %v", out, ok)
	}
	if _, ok := parseTranslationArray(`["a"]`, 2); ok {
		t.Error("a wrong length must be rejected")
	}
	if _, ok := parseTranslationArray("no array", 1); ok {
		t.Error("text without an array must be rejected")
	}
}
