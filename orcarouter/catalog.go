package orcarouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Capability names used by the model pickers.
const (
	CapabilityChat      = "chat"
	CapabilityEmbedding = "embedding"
	CapabilityImage     = "image"
	CapabilityVideo     = "video"
	CapabilityRerank    = "rerank"
)

// Model is the subset of the catalog record the pickers need. Unknown fields
// are ignored, and unknown endpoint types never grant a capability.
type Model struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name,omitempty"`
	EndpointTypes        []string `json:"supported_endpoint_types,omitempty"`
	EndpointCapabilities []string `json:"endpoint_capabilities,omitempty"`
	InputModalities      []string `json:"input_modalities,omitempty"`
	ContextLength        int      `json:"context_length,omitempty"`
	MaxCompletionTokens  int      `json:"max_completion_tokens,omitempty"`
	ReasoningEfforts     []string `json:"reasoning_efforts,omitempty"`
}

type catalogEnvelope struct {
	Data []struct {
		ID                     string   `json:"id"`
		Name                   string   `json:"name"`
		SupportedEndpointTypes []string `json:"supported_endpoint_types"`
		EndpointCapabilities   []string `json:"endpoint_capabilities"`
		ContextLength          int      `json:"context_length"`
		MaxCompletionTokens    int      `json:"max_completion_tokens"`
		Architecture           struct {
			InputModalities []string `json:"input_modalities"`
		} `json:"architecture"`
		TopProvider struct {
			ContextLength       int `json:"context_length"`
			MaxCompletionTokens int `json:"max_completion_tokens"`
		} `json:"top_provider"`
		SupportedParameters []string `json:"supported_parameters"`
	} `json:"data"`
}

const (
	maxCatalogBytes = 4 << 20
	maxCatalogItems = 1000
)

// FetchCatalog performs a bounded GET {APIBase}/models with Bearer auth.
func FetchCatalog(ctx context.Context, client *http.Client, apiBase, apiKey string) ([]Model, error) {
	if strings.TrimSpace(apiBase) == "" {
		return nil, errors.New("orcarouter: API base is not configured")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("orcarouter: API key is required for model discovery")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiBase, "/")+"/models", nil)
	if err != nil {
		return nil, errors.New("orcarouter: building model discovery request failed")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("orcarouter: model discovery request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("orcarouter: model discovery returned status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes))
	if err != nil {
		return nil, errors.New("orcarouter: reading model catalog failed")
	}
	return ParseCatalog(raw)
}

// ParseCatalog decodes the catalog envelope and keeps only well-formed,
// non-duplicate records. List length is capped so a hostile endpoint cannot
// consume unbounded memory.
func ParseCatalog(raw []byte) ([]Model, error) {
	var env catalogEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, errors.New("orcarouter: model catalog was not valid JSON")
	}
	out := make([]Model, 0, len(env.Data))
	seen := make(map[string]struct{}, len(env.Data))
	for _, m := range env.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || strings.ContainsAny(id, " \t\r\n") {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ctxLen := m.ContextLength
		if ctxLen == 0 {
			ctxLen = m.TopProvider.ContextLength
		}
		maxOut := m.MaxCompletionTokens
		if maxOut == 0 {
			maxOut = m.TopProvider.MaxCompletionTokens
		}
		model := Model{
			ID:                   id,
			Name:                 strings.TrimSpace(m.Name),
			EndpointTypes:        lowerAll(m.SupportedEndpointTypes),
			EndpointCapabilities: lowerAll(m.EndpointCapabilities),
			InputModalities:      lowerAll(m.Architecture.InputModalities),
			ContextLength:        ctxLen,
			MaxCompletionTokens:  maxOut,
		}
		if hasReasoning(m.SupportedParameters) {
			// Verified from the public catalog: GPT-5.5-class reasoning models
			// advertise the full ladder.
			model.ReasoningEfforts = []string{"low", "medium", "high", "xhigh"}
		}
		out = append(out, model)
		if len(out) >= maxCatalogItems {
			break
		}
	}
	return out, nil
}

// Filter returns the models compatible with one UI capability. Unsupported
// records fail closed rather than being guessed from the model name.
func Filter(models []Model, capability, inputModality string) []Model {
	capability = strings.ToLower(strings.TrimSpace(capability))
	if capability == "" {
		capability = CapabilityChat
	}
	modality := strings.ToLower(strings.TrimSpace(inputModality))
	out := make([]Model, 0, len(models))
	for _, m := range models {
		if !supportsCapability(m, capability) {
			continue
		}
		if modality != "" && !containsFold(m.InputModalities, modality) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func supportsCapability(m Model, capability string) bool {
	switch capability {
	case CapabilityChat:
		// Text/multimodal chat only: the endpoint must speak a chat wire format
		// and must not be a single-purpose media/embedding route.
		speaksChat := false
		for _, t := range m.EndpointTypes {
			switch t {
			case "openai", "anthropic", "gemini", "openai-response", "openai-responses":
				speaksChat = true
			case "embeddings", "image-generation", "openai-video", "jina-rerank":
				return false
			}
		}
		return speaksChat
	case CapabilityEmbedding:
		return hasAny(m.EndpointTypes, "embeddings") || hasAny(m.EndpointCapabilities, "embeddings")
	case CapabilityImage:
		return hasAny(m.EndpointTypes, "image-generation")
	case CapabilityVideo:
		return hasAny(m.EndpointTypes, "openai-video", "openai-video-generation")
	case CapabilityRerank:
		return hasAny(m.EndpointTypes, "jina-rerank")
	default:
		// Unknown capability: advertise nothing rather than guess.
		return false
	}
}

// Seed is the small, verified cold-start catalog used only when live
// discovery fails. It is explicitly marked degraded by callers and is never
// merged into a successful live result.
func Seed() []Model {
	return []Model{
		{ID: "orcarouter/auto", Name: "OrcaRouter Auto", EndpointTypes: []string{"openai", "openai-response", "anthropic", "gemini"}, InputModalities: []string{"text", "image", "file"}, ContextLength: 200000, ReasoningEfforts: []string{"low", "medium", "high", "xhigh"}},
		{ID: "orcarouter/fusion", Name: "OrcaRouter Fusion", EndpointTypes: []string{"openai", "openai-response", "anthropic", "gemini"}, InputModalities: []string{"text", "image", "file"}, ContextLength: 200000},
		{ID: "openai/gpt-5.5", Name: "OpenAI: GPT-5.5", EndpointTypes: []string{"openai", "openai-response"}, InputModalities: []string{"text", "image", "file"}, ContextLength: 272000, MaxCompletionTokens: 128000, ReasoningEfforts: []string{"low", "medium", "high", "xhigh"}},
		{ID: "anthropic/claude-opus-4.8", Name: "Anthropic: Claude Opus 4.8", EndpointTypes: []string{"openai", "anthropic", "openai-response"}, InputModalities: []string{"text", "image", "file"}, ContextLength: 200000},
		{ID: "google/gemini-3.5-flash", Name: "Google: Gemini 3.5 Flash", EndpointTypes: []string{"openai", "gemini", "openai-response"}, InputModalities: []string{"text", "image", "file", "audio", "video"}, ContextLength: 1000000},
		{ID: "deepseek/deepseek-v4-pro", Name: "DeepSeek: V4 Pro", EndpointTypes: []string{"openai", "anthropic"}, InputModalities: []string{"text"}, ContextLength: 128000},
		{ID: "orcacyber-ultra-1.0", Name: "OrcaCyber Ultra", EndpointTypes: []string{"openai", "anthropic", "openai-response"}, InputModalities: []string{"text", "image", "file"}, ContextLength: 200000, ReasoningEfforts: []string{"low", "medium", "high", "xhigh"}},
		{ID: "orcacyber-zero-1.5", Name: "OrcaCyber Zero", EndpointTypes: []string{"openai", "anthropic", "openai-response"}, InputModalities: []string{"text", "image", "file"}, ContextLength: 128000},
	}
}

// DefaultModel is the documented default for the OrcaRouter provider: it is
// always selectable even on workspaces whose catalog omits it.
const DefaultModel = "orcarouter/auto"

func hasAny(values []string, want ...string) bool {
	for _, v := range values {
		for _, w := range want {
			if strings.EqualFold(v, w) {
				return true
			}
		}
	}
	return false
}

func containsFold(values []string, want string) bool {
	return hasAny(values, want)
}

func lowerAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, strings.ToLower(strings.TrimSpace(v)))
	}
	return out
}

func hasReasoning(params []string) bool {
	for _, p := range params {
		if strings.EqualFold(p, "reasoning") || strings.EqualFold(p, "include_reasoning") {
			return true
		}
	}
	return false
}
