package notify

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This file pins the **capability boundary** of the generic Webhook template.
//
// It is the only place in the package where a user-supplied string is evaluated as code, so
// what it can and cannot do must be explicit and fixed by tests — otherwise someone later
// adds a method to the template context, or a readFile to the FuncMap, and the capability
// surface silently grows while the diff looks like one harmless little function.

// TestTemplateContextHasNoMethods is the most important one.
//
// text/template calls exported methods ({{.Foo}} reads a field or calls a method). So as soon
// as the template context can reach **any** type with exported methods, those methods are
// exposed to the template author. This feature's context is deliberately pure data (exported
// fields only, zero methods).
//
// If this fails: someone added a method to webhookTemplateData / webhookItem. Before allowing
// it, work out whether the template author could use that method to read something that
// should not be exposed.
func TestTemplateContextHasNoMethods(t *testing.T) {
	for _, v := range []any{webhookTemplateData{}, webhookItem{}} {
		typ := reflect.TypeOf(v)
		if n := typ.NumMethod(); n != 0 {
			var names []string
			for i := 0; i < n; i++ {
				names = append(names, typ.Method(i).Name)
			}
			t.Fatalf("%s exposes %d methods (%s): text/template can call them, which hands their "+
				"capabilities to the template author", typ.Name(), n, strings.Join(names, ", "))
		}
	}
}

// TestTemplateFuncsAreMinimal pins the set of functions exposed to templates.
//
// Every extra function in the FuncMap is one more capability. Currently only json / jsons,
// which serialize a value into a JSON fragment — no file reads, no requests, no commands.
func TestTemplateFuncsAreMinimal(t *testing.T) {
	var got []string
	for name := range webhookTemplateFuncs {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"json", "jsons"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the template function set changed: got %v, want %v. Before adding a function, "+
			"confirm it does not widen the capability surface (no file reads or writes, no network "+
			"requests, no command execution)", got, want)
	}
}

// TestTemplateCannotReachUnknownData covers out-of-bounds access in templates: touching
// something that does not exist must fail rather than echo something back, and the failure
// must not carry internal data out.
func TestTemplateCannotReachUnknownData(t *testing.T) {
	_, err := renderWebhookBody(`{"x": {{.Environment}}, "y": {{.Env}}}`, singleMsg())
	if err == nil {
		t.Fatal("accessing a nonexistent field must fail")
	}
	// The error must not contain real content from the template context (finding title/summary).
	for _, leak := range []string{"SQL injection", "parameter id"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the template error leaked message content %q: %v", leak, err)
		}
	}
}

// TestTemplateRenderFailsPermanently: a broken template is a configuration error, and retrying
// never heals it. Judging it retryable would burn three backoff rounds on every delivery of one
// bad template.
func TestTemplateRenderFailsPermanently(t *testing.T) {
	cfg := map[string]any{
		"url":           "https://example.com/hook",
		"body_template": `{{.Items.`,
	}
	if err := (webhookChannel{}).Validate(cfg); err == nil {
		t.Fatal("a template syntax error must be rejected at save time")
	}
	// Even bypassing validation and sending directly, it must be judged permanent, not retried.
	_, err := (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("a broken template must be a permanent failure, got %v", err)
	}
}

// TestTemplateCanOnlyProduceJSON covers the constraint that a template render must be valid
// JSON. It also blocks using templates to produce plain text that drives some other protocol.
func TestTemplateCanOnlyProduceJSON(t *testing.T) {
	// A valid template passes.
	ok := map[string]any{"url": "https://example.com/hook", "body_template": `{"t":{{json .Title}}}`}
	if err := (webhookChannel{}).Validate(ok); err != nil {
		t.Fatalf("a valid template must pass validation: %v", err)
	}
	// A non-JSON render must be rejected (not sent as-is).
	bad := map[string]any{"url": "http://127.0.0.1:1/hook", "body_template": `not json {{.Count}}`}
	_, err := (webhookChannel{}).Send(context.Background(), bad, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("a non-JSON render must be a permanent failure, got %v", err)
	}
	// Language-neutral: the wording of the validation error is not pinned, only that it names JSON.
	if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("the error message must say the problem is JSON, got %v", err)
	}
}
