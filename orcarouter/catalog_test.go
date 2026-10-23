package orcarouter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const catalogFixture = `{
  "data": [
    {"id":"vendor/text-only","supported_endpoint_types":["openai","anthropic"],"architecture":{"input_modalities":["text"]},"context_length":128000,"supported_parameters":["tools"]},
    {"id":"vendor/vision","supported_endpoint_types":["openai","gemini"],"architecture":{"input_modalities":["text","image"]},"context_length":200000},
    {"id":"vendor/vidvision","supported_endpoint_types":["openai"],"architecture":{"input_modalities":["text","image","video"]},"context_length":200000},
    {"id":"vendor/unknown-modality","supported_endpoint_types":["openai"]},
    {"id":"vendor/embed","supported_endpoint_types":["embeddings"],"architecture":{"input_modalities":["text"]}},
    {"id":"vendor/img-gen","supported_endpoint_types":["image-generation"]},
    {"id":"vendor/video-gen","supported_endpoint_types":["openai-video"]},
    {"id":"vendor/rerank","supported_endpoint_types":["jina-rerank"]},
    {"id":"vendor/legacy","supported_endpoint_types":["image-generation","openai"]},
    {"id":"vendor/text-only","supported_endpoint_types":["openai"]},
    {"id":"  ","supported_endpoint_types":["openai"]},
    {"id":"bad id with spaces","supported_endpoint_types":["openai"]}
  ]
}`

func ids(models []Model) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func hasID(models []Model, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}

func TestParseCatalogFiltersMalformedAndDuplicates(t *testing.T) {
	models, err := ParseCatalog([]byte(catalogFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := ids(models)
	for _, bad := range []string{"", "  ", "bad id with spaces"} {
		if hasID(models, bad) {
			t.Fatalf("malformed id kept: %q", bad)
		}
	}
	count := 0
	for _, id := range got {
		if id == "vendor/text-only" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate ids not collapsed: %v", got)
	}
	if _, err := ParseCatalog([]byte(`{`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestFilterChatExcludesNonTextEndpoints(t *testing.T) {
	models, _ := ParseCatalog([]byte(catalogFixture))
	chat := Filter(models, CapabilityChat, "")
	for _, banned := range []string{"vendor/embed", "vendor/img-gen", "vendor/video-gen", "vendor/rerank", "vendor/legacy"} {
		if hasID(chat, banned) {
			t.Fatalf("non-chat model %q leaked into the chat picker", banned)
		}
	}
	for _, want := range []string{"vendor/text-only", "vendor/vision", "vendor/vidvision", "vendor/unknown-modality"} {
		if !hasID(chat, want) {
			t.Fatalf("chat model %q missing", want)
		}
	}
}

func TestFilterMultimodalFailsClosed(t *testing.T) {
	models, _ := ParseCatalog([]byte(catalogFixture))
	visible := Filter(models, CapabilityChat, "image")
	if hasID(visible, "vendor/text-only") {
		t.Fatal("text-only model included in image picker")
	}
	if hasID(visible, "vendor/unknown-modality") {
		t.Fatal("model without declared image input included (must fail closed)")
	}
	if !hasID(visible, "vendor/vision") || !hasID(visible, "vendor/vidvision") {
		t.Fatalf("image-capable chat models missing: %v", ids(visible))
	}
	video := Filter(models, CapabilityChat, "video")
	if hasID(video, "vendor/vision") || !hasID(video, "vendor/vidvision") {
		t.Fatalf("video filter wrong: %v", ids(video))
	}
}

func TestFilterCapabilitySpecificEndpoints(t *testing.T) {
	models, _ := ParseCatalog([]byte(catalogFixture))
	cases := map[string][]string{
		CapabilityEmbedding: {"vendor/embed"},
		CapabilityImage:     {"vendor/img-gen", "vendor/legacy"},
		CapabilityVideo:     {"vendor/video-gen"},
		CapabilityRerank:    {"vendor/rerank"},
	}
	for capability, want := range cases {
		got := Filter(models, capability, "")
		if len(got) != len(want) {
			t.Fatalf("%s filter = %v, want %v", capability, ids(got), want)
		}
		for _, id := range want {
			if !hasID(got, id) {
				t.Fatalf("%s filter = %v, missing %s", capability, ids(got), id)
			}
		}
	}
	if got := Filter(models, "telepathy", ""); len(got) != 0 {
		t.Fatalf("unknown capability must advertise nothing, got %v", ids(got))
	}
}

func TestSeedKeepsVerifiedMetadata(t *testing.T) {
	seed := Seed()
	var gpt *Model
	for i := range seed {
		if seed[i].ID == "openai/gpt-5.5" {
			gpt = &seed[i]
		}
	}
	if gpt == nil {
		t.Fatal("seed lost openai/gpt-5.5")
	}
	if len(gpt.ReasoningEfforts) != 4 {
		t.Fatalf("gpt-5.5 reasoning ladder = %v", gpt.ReasoningEfforts)
	}
	if !containsFold(gpt.InputModalities, "image") {
		t.Fatal("gpt-5.5 lost image modality")
	}
	if !hasID(Filter(seed, CapabilityChat, ""), DefaultModel) {
		t.Fatal("seed must keep the documented default model")
	}
	img := Filter(seed, CapabilityChat, "image")
	if hasID(img, "deepseek/deepseek-v4-pro") {
		t.Fatal("text-only seed model leaked into the image picker")
	}
	if !hasID(img, "openai/gpt-5.5") {
		t.Fatal("image-capable seed model missing")
	}
}

func TestFetchCatalogUsesBearerAndModelsPath(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, catalogFixture)
	}))
	defer srv.Close()
	models, err := FetchCatalog(context.Background(), srv.Client(), srv.URL+"/v1", "sk-orca-test-key")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path = %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer sk-orca-test-key" {
		t.Fatalf("authorization header not set")
	}
	if len(models) == 0 {
		t.Fatal("no models parsed")
	}
}

func TestFetchCatalogRejectsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, err := FetchCatalog(context.Background(), srv.Client(), srv.URL+"/v1", "sk-orca-test-key"); err == nil {
		t.Fatal("401 catalog must be an error (caller then uses the seed)")
	}
}

func TestCatalogIDsPreserveNamespace(t *testing.T) {
	models, _ := ParseCatalog([]byte(catalogFixture))
	if !hasID(models, "vendor/text-only") {
		t.Fatal("vendor/model namespace not preserved")
	}
}
