package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// OOBInteraction is one callback the OOB service recorded for our probes.
type OOBInteraction struct {
	Protocol     string `json:"protocol"`
	UniqueID     string `json:"probe_id"`
	FullID       string `json:"full_id,omitempty"`
	Remote       string `json:"remote,omitempty"`
	Raw          string `json:"raw,omitempty"`
	TimestampISO string `json:"timestamp,omitempty"`
}

// oobSession is the seam between the tools and the Interactsh client.
type oobSession interface {
	// Generate returns (probeID, canonicalHost) for one fresh callback address.
	Generate() (string, string, error)
	// Poll waits up to `wait` for new interactions and returns them.
	Poll(wait time.Duration) ([]OOBInteraction, error)
	Close() error
}

const (
	oobMaxAddresses = 5
	oobDefaultWait  = 5 * time.Second
	oobMaxWait      = 30 * time.Second
)

// oobSeededTools builds the catalog shells (nil session) used only to seed
// Name/Description/Schema into the tools table; they are never called.
func (t *ToolSet) oobSeededTools() []actool.CoreTool {
	return []actool.CoreTool{t.oobGenerate(nil), t.oobPoll(nil)}
}

// oobGenerate issues fresh callback addresses. count defaults to 1, max 5.
func (t *ToolSet) oobGenerate(s oobSession) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "oob_generate",
		Description: "生成本次执行专用的带外（OOB）回调地址（Interactsh）。用于反序列化、SSRF、SSTI/XXE/文件读取等需要带外确认的验证：把回调域名嵌进 payload 发给目标，再用 oob_poll 查是否收到回调。地址仅本 run 有效；一条验证需要多个独立探测时才生成多个，默认 1 个。",
		Schema: obj(map[string]any{
			"count": map[string]any{"type": "integer", "description": "生成个数，默认 1，最多 5"},
		}),
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Count int `json:"count"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Count < 0 || a.Count > oobMaxAddresses {
				return actool.Errorf(fmt.Sprintf("count 必须在 1–%d 之间（默认 1）", oobMaxAddresses)), nil
			}
			if a.Count == 0 {
				a.Count = 1
			}
			probes := make([]map[string]string, 0, a.Count)
			for i := 0; i < a.Count; i++ {
				id, host, err := s.Generate()
				if err != nil {
					if saveErr := t.recordOOBUnavailable(err); saveErr != nil {
						return actool.Errorf(err.Error()), nil
					}
					return jsonResult(map[string]any{
						"probes": probes,
						"status": "service_unavailable",
						"error":  err.Error(),
					})
				}
				probes = append(probes, map[string]string{"probe_id": id, "url": host, "domain": host, "http_url": "http://" + host})
			}
			return jsonResult(map[string]any{
				"probes": probes,
				"hint":   "把回调域名嵌入 payload 发送到目标后，用 oob_poll 等待回调；收到回调只是中间证据，仍需按标准卡完成危害取证。",
			})
		},
	})
}

// oobPoll waits (bounded) for OOB callbacks and returns them.
func (t *ToolSet) oobPoll(s oobSession) actool.CoreTool {
	var mu sync.Mutex
	var pending []OOBInteraction
	return actool.Build(actool.Spec{
		Name:        "oob_poll",
		Description: "等待并返回带外回调（配合 oob_generate）。轮询有界等待；只有健康完成的观察窗口才敢下'暂无回调'的结论——服务不可用会明确报 service_unavailable，不要把故障当成目标无漏洞。匹配回调和本轮空窗口会作为带外/否定事实自动写回当前意图；回调只是中间证据，仍需继续验证实际危害，不自动生成 finding 或 severity。",
		Schema: obj(map[string]any{
			"probe_id": str("可选：仅返回指定探测的交互，其他探测交互保留供后续查询"),
			"wait":     map[string]any{"type": "integer", "description": fmt.Sprintf("等待秒数，默认 %d，最多 %d", int(oobDefaultWait.Seconds()), int(oobMaxWait.Seconds()))},
		}),
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			mu.Lock()
			defer mu.Unlock()
			var a struct {
				Wait    int    `json:"wait"`
				ProbeID string `json:"probe_id"`
			}
			_ = json.Unmarshal(in, &a)
			wait := oobDefaultWait
			if a.Wait > 0 {
				if a.Wait > int(oobMaxWait.Seconds()) {
					return actool.Errorf(fmt.Sprintf("wait 最多 %d 秒", int(oobMaxWait.Seconds()))), nil
				}
				wait = time.Duration(a.Wait) * time.Second
			}
			observationFailed := func(err error) (actool.Result, error) {
				outcome := "service_unavailable"
				summary := "OOB 服务不可用，本轮未证明目标无漏洞"
				if errors.Is(err, errNoCompletedPoll) || errors.Is(err, errSessionClosed) || errors.Is(err, errStaleProbe) {
					outcome = "observation_incomplete"
					summary = "OOB 观察窗口未完成，不能判断是否有回调"
				}
				out := map[string]any{
					"interactions": []OOBInteraction{},
					"status":       outcome,
					"error":        err.Error(),
				}
				switch {
				case errors.Is(err, errSessionClosed):
					out["hint"] = "本次 OOB 会话已关闭（run 结束或重启失效），观察窗口未完成；继续验证请用 oob_generate 重新分配回调地址。"
				case errors.Is(err, errStaleProbe):
					out["hint"] = "probe_id не выдан в текущей сессии (например, пришёл из восстановленной истории воркера). Выполните oob_generate заново и используйте новый адрес."
				case outcome == "service_unavailable":
					out["hint"] = "OOB 服务当前不可用，不影响其他验证；如需稳定带外验证可在 config.json 的 oob 块配置自建 Interactsh 服务（server_url + token），或稍后重试。"
				case outcome == "observation_incomplete":
					out["hint"] = "观察窗口内没有完成一次轮询，常由 OOB 服务故障引起；可稍后重试，或配置自建 Interactsh 服务（config.json 的 oob 块）。"
				}
				if id, saveErr := t.recordOOBOutcome(outcome, summary, "", err.Error()); saveErr == nil && id > 0 {
					out["fact_id"] = id
				}
				return jsonResult(out)
			}

			// A probe ID from a resumed transcript belongs to an earlier
			// session; this run cannot observe it.
			if a.ProbeID != "" {
				if ts, ok := s.(*trackedSession); ok && !ts.knows(a.ProbeID) {
					return observationFailed(errStaleProbe)
				}
			}

			// Retained callbacks answer first: another probe's callback must not
			// force a remote poll that could fail or observe nothing.
			its := []OOBInteraction{}
			if a.ProbeID != "" {
				remaining := make([]OOBInteraction, 0, len(pending))
				for _, interaction := range pending {
					if interaction.UniqueID == a.ProbeID {
						its = append(its, interaction)
					} else {
						remaining = append(remaining, interaction)
					}
				}
				pending = remaining
			}
			if len(its) == 0 {
				// Keep observing until the requested window completes: a batch
				// holding another probe's callback says nothing about this probe
				// and must not be recorded as a healthy empty window.
				deadline := time.Now().Add(wait)
				healthy := false
				for {
					polled, err := s.Poll(time.Until(deadline))
					if err != nil {
						// A successful poll already completed during this
						// observation; a later quiet tick must not erase it.
						if errors.Is(err, errNoCompletedPoll) && healthy && !time.Now().Before(deadline) {
							break
						}
						return observationFailed(err)
					}
					healthy = true
					pending = append(pending, polled...)
					remaining := make([]OOBInteraction, 0, len(pending))
					for _, interaction := range pending {
						if a.ProbeID == "" || interaction.UniqueID == a.ProbeID {
							its = append(its, interaction)
						} else {
							remaining = append(remaining, interaction)
						}
					}
					pending = remaining
					if len(its) > 0 || !time.Now().Before(deadline) {
						break
					}
				}
			}
			out := map[string]any{
				"interactions": its,
				"status":       "ok",
				"count":        strconv.Itoa(len(its)),
			}
			outcome := "no_interaction"
			summary := "OOB 观察窗口内暂无回调（不代表目标无漏洞）"
			detail := fmt.Sprintf("在健康完成的 %s 观察窗口内未收到匹配回调。", wait)
			if len(its) > 0 {
				outcome = "progress"
				summary = fmt.Sprintf("收到 %d 条带外交互（中间证据，尚无最终危害）", len(its))
				detail = oobInteractionDetail(its)
			}
			evidence := ""
			if len(its) > 0 {
				evidence = detail
			}
			if id, saveErr := t.recordOOBOutcome(outcome, summary, detail, evidence); saveErr == nil && id > 0 {
				out["fact_id"] = id
			}
			return jsonResult(out)
		},
	})
}

func oobInteractionDetail(its []OOBInteraction) string {
	parts := make([]string, 0, len(its))
	for _, it := range its {
		part := it.Protocol + " probe=" + it.UniqueID
		if it.Remote != "" {
			part += " remote=" + it.Remote
		}
		if it.TimestampISO != "" {
			part += " at=" + it.TimestampISO
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

func oobToolsForRun(t *ToolSet, s oobSession) []actool.CoreTool {
	if s == nil || t == nil {
		return nil
	}
	tracked := &trackedSession{inner: s, issued: make(map[string]struct{})}
	return []actool.CoreTool{t.oobGenerate(tracked), t.oobPoll(tracked)}
}

// recordOOBOutcome persists one handler-owned route fact while retaining the
// worker's intent. It deliberately stores interaction metadata only: raw
// callback bodies may contain target data or secrets and remain in the trace.
func (t *ToolSet) recordOOBOutcome(outcome, summary, detail, evidence string) (int64, error) {
	if t == nil || t.ts == nil || t.ownerNode <= 0 {
		return 0, nil
	}
	payload := map[string]any{
		"summary":    summary,
		"detail":     detail,
		"confidence": "observed",
		"outcome":    outcome,
		"route": map[string]any{
			"outcome": outcome,
		},
	}
	if evidence != "" {
		payload["evidence"] = evidence
	}
	if parent, err := t.ts.GetNode(t.ownerNode); err == nil && parent != nil {
		var pp map[string]any
		if json.Unmarshal(parent.Payload, &pp) == nil {
			if route, ok := pp["route"]; ok {
				payload["route"] = route
				if m, ok := payload["route"].(map[string]any); ok {
					m["outcome"] = outcome
				}
			}
		}
	}
	id, err := t.ts.AddNode(db.KindFact, payload, 5, "confirmed", t.worker, nil)
	if err != nil {
		return 0, err
	}
	if err := t.ts.Link(t.ownerNode, db.RelYields, id); err != nil {
		return id, err
	}
	t.writes.Facts++
	return id, nil
}

func (t *ToolSet) recordOOBUnavailable(err error) error {
	if t == nil || t.ts == nil || t.ownerNode <= 0 {
		return nil
	}
	if _, saveErr := t.recordOOBOutcome("service_unavailable", "OOB 服务不可用，本轮未证明目标无漏洞", "", err.Error()); saveErr != nil {
		return saveErr
	}
	return nil
}
