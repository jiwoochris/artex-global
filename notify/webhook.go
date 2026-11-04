package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// webhookChannel is the generic webhook adapter: the user picks the URL, method,
// headers, and JSON template. Its existence means this feature does not need a
// separate implementation for Slack / Mattermost / Discord / in-house systems —
// all of those are covered by a configurable template.
type webhookChannel struct{}

func (webhookChannel) Kind() string { return KindWebhook }

// The generic webhook has no official limit; 0 means no rate limit by default and
// the user tunes it to what the receiver can handle.
func (webhookChannel) DefaultRatePerMin() int { return 0 }

// Mask url and headers: the target address often carries a token itself and custom
// headers usually hold authentication credentials, and both are echoed back by the
// API, so both must be blocked.
// The cost is that changing one header requires re-entering the whole header set
// (a masked value is interpreted as "keep the stored value") — a deliberate
// tradeoff: filling it in once more beats echoing credentials to the browser.
func (webhookChannel) SecretKeys() []string { return []string{"url", "headers"} }

// The destination is url. Changing url requires re-confirming headers — otherwise
// the original Authorization header would be sent verbatim to the new address,
// which is precisely the main masking-bypass path.
func (webhookChannel) DestinationKeys() []string { return []string{"url"} }

// webhookDefaultTemplate is the fallback body when no template is configured: a
// plain JSON structure that covers the overwhelming majority of "store one JSON
// row" in-house receivers.
const webhookDefaultTemplate = `{
  "title": {{json .Title}},
  "batch": {{.Batch}},
  "count": {{.Count}},
  "items": [
{{- range $i, $it := .Items}}
{{- if $i}},{{end}}
    {
      "finding_id": {{$it.FindingID}},
      "name": {{json $it.Name}},
      "vulnclass": {{json $it.VulnClass}},
      "severity": {{json $it.Severity}},
      "summary": {{json $it.Summary}},
      "assets": {{json $it.Assets}},
      "detail_url": {{json $it.DetailURL}}
    }
{{- end}}
  ]
}`

// webhookTemplateData is the context exposed to user templates.
type webhookTemplateData struct {
	Title   string
	Batch   bool
	Count   int
	Items   []webhookItem
	HomeURL string
	// SentAt is the delivery time (RFC3339), for the receiver to record.
	SentAt string
}

type webhookItem struct {
	FindingID     int64
	Name          string
	VulnClass     string
	Severity      string
	SeverityLabel string
	Summary       string
	Assets        []string
	DetailURL     string
	FromStatus    string
	ToStatus      string
	// StatusLabel is the readable description of a status change, such as
	// "Pending → Fixed"; empty when this is not a status change.
	StatusLabel string
}

func (webhookChannel) Validate(cfg map[string]any) error {
	raw := cfgString(cfg, "url")
	if raw == "" {
		return errors.New("The target URL is missing")
	}
	if err := validateHTTPURL(raw); err != nil {
		return fmt.Errorf("The target URL is invalid: %w", err)
	}
	if m := strings.ToUpper(cfgString(cfg, "method")); m != "" && m != http.MethodGet && m != http.MethodPost && m != http.MethodPut && m != http.MethodPatch {
		return fmt.Errorf("Unsupported method: %s (choose GET/POST/PUT/PATCH)", m)
	}
	if tpl := cfgString(cfg, "body_template"); tpl != "" {
		if _, err := parseWebhookTemplate(tpl); err != nil {
			return fmt.Errorf("The request body template has a syntax error: %w", err)
		}
	}
	return nil
}

func (c webhookChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	method := strings.ToUpper(cfgString(cfg, "method"))
	if method == "" {
		method = http.MethodPost
	}

	// GET carries no body: packing the content into the query string is beyond what
	// a template can do and violates GET semantics, so GET only suits receivers
	// where the request itself is the trigger.
	var payload any
	if method != http.MethodGet {
		body, err := renderWebhookBody(cfgString(cfg, "body_template"), m)
		if err != nil {
			return 0, Permanent(err)
		}
		// The template renders JSON as a string; it is converted to json.RawMessage
		// and sent as-is so double escaping cannot wrap the carefully constructed
		// structure inside a JSON string.
		if !json.Valid([]byte(body)) {
			return 0, Permanent(errors.New("The rendered request body template is not valid JSON"))
		}
		payload = json.RawMessage(body)
	}

	headers := cfgMap(cfg, "headers")
	if ct := cfgString(cfg, "content_type"); ct != "" {
		// Overriding is allowed, but applied after headers so an explicit setting
		// wins.
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = ct
	}
	if _, err := doJSON(ctx, method, cfgString(cfg, "url"), headers, payload); err != nil {
		return 0, err
	}
	// The generic webhook never truncates the body (the receiver is the user's own
	// service and the size is determined by body_template), so the whole batch
	// counts as delivered.
	return len(m.Items), nil
}

// renderWebhookBody renders the request body with the user template (or the default
// one).
func renderWebhookBody(tpl string, m Message) (string, error) {
	if strings.TrimSpace(tpl) == "" {
		tpl = webhookDefaultTemplate
	}
	t, err := parseWebhookTemplate(tpl)
	if err != nil {
		return "", fmt.Errorf("The request body template has a syntax error: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, newWebhookTemplateData(m)); err != nil {
		return "", fmt.Errorf("Failed to render the request body template: %w", err)
	}
	return buf.String(), nil
}

// parseWebhookTemplate parses the template.
//
// missingkey=zero renders absent map keys as zero values instead of failing — but
// the context here is a struct, so its main effect is keeping range over an empty
// .Items from erroring. What really needs guarding is .Items being nil.
func parseWebhookTemplate(tpl string) (*template.Template, error) {
	return template.New("body").Funcs(webhookTemplateFuncs).Option("missingkey=zero").Parse(tpl)
}

// webhookTemplateFuncs are the helper functions exposed to templates.
var webhookTemplateFuncs = template.FuncMap{
	// json serializes any value as JSON.
	//
	// This function is not a nicety, it is required: without it users could only
	// interpolate {{.Title}} directly, and a single quote or newline in a finding
	// title would make the whole request body invalid JSON. The receiver would
	// reject it with a "JSON parse failed" error that gives no hint the cause was a
	// quote inside the title.
	"json": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	},
	// jsons embeds a JSON fragment inside another JSON string value (one level of
	// string escaping).
	"jsons": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		quoted, err := json.Marshal(string(raw))
		if err != nil {
			return "", err
		}
		// Strip the surrounding quotes: the caller decides whether to add quotes.
		return string(quoted[1 : len(quoted)-1]), nil
	},
}

func newWebhookTemplateData(m Message) webhookTemplateData {
	d := webhookTemplateData{
		Title:   markdownTitle(m),
		Batch:   m.Batch,
		Count:   len(m.Items),
		HomeURL: m.HomeURL,
		SentAt:  time.Now().Format(time.RFC3339),
		Items:   make([]webhookItem, 0, len(m.Items)),
	}
	for _, it := range m.Items {
		wi := webhookItem{
			FindingID:     it.FindingID,
			Name:          it.Name,
			VulnClass:     it.VulnClass,
			Severity:      it.Severity,
			SeverityLabel: SeverityLabel(it.Severity),
			Summary:       it.Summary,
			Assets:        append([]string{}, it.Assets...),
			DetailURL:     it.DetailURL,
			FromStatus:    it.FromStatus,
			ToStatus:      it.ToStatus,
		}
		if it.IsStatusChange() {
			wi.StatusLabel = StatusLabel(it.FromStatus) + " → " + StatusLabel(it.ToStatus)
		}
		d.Items = append(d.Items, wi)
	}
	return d
}
