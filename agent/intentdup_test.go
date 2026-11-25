package agent

import (
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func intentNode(id int64, state, summary string, anchors ...int64) *db.Node {
	p, _ := json.Marshal(map[string]any{"summary": summary, "asset_ids": anchors})
	// Production shape: ListByKind selects no anchors column, so Anchors stays
	// empty and the payload is the only source of target assets.
	return &db.Node{ID: id, Kind: db.KindIntent, State: state, Payload: p}
}

// The production failure: the same direction re-issued while an open copy sits
// in the queue (METASCAN #3368 vs #3362, TCI #3489 vs #3484).
func TestDuplicateOpenIntentBlocksQueuedDirection(t *testing.T) {
	queued := intentNode(3484, "open",
		"SQL-инъекции на https://lk.cainet.ru: систематически проверить параметры billmgr (func, elid, plid, ptype, sel, filter, ord, p_num, search, chapter, item, startform, lang, theme, out, sok) и кастомных модулей (v2.*.order.param, check_domain, domain.order.param, certificate.order.param, mhost.order.param, tcihostingdns.order.param, long/{token}) слепыми и time-based методами с отрицательным контролем и контролируемой задержкой; цель — версия СУБД и current_user, чтение системных таблиц.", 4175, 4015)
	again := "Проверить SQL-инъекции в параметрах https://lk.cainet.ru/billmgr и кастомных модулей ТЦИ (sel, filter, ord, p_num, search, chapter, item, startform, lang, theme, out, sok, поля v2.*.order.param/check_domain/domain.order.param/certificate.order.param/mhost.order.param/tcihostingdns.order.param) в гостевой (level 8) и авторизованной (level 16, account 899) сессии: слепые и time-based проверки с контролируемой задержкой (SLEEP/pg_sleep) и отрицательным контролем, без перебора идентификаторов и разрушающих запросов; цель — версия СУБД, current_user и чтение системных таблиц как воспроизводимое доказательство."
	id, _, dup := duplicateOpenIntent([]*db.Node{queued}, again, []int64{4175, 4015})
	if !dup || id != 3484 {
		t.Fatalf("queued duplicate not blocked: dup=%v id=%d", dup, id)
	}
}

// A stopped intent is a forced interruption, not a verdict on the direction:
// the same direction may be issued again (owner rule).
func TestDuplicateOpenIntentAllowsStoppedDirection(t *testing.T) {
	stopped := intentNode(3484, "stopped",
		"SQL-инъекции на https://lk.cainet.ru: систематически проверить параметры billmgr", 4175, 4015)
	again := "SQL-инъекции на https://lk.cainet.ru: систематически проверить параметры billmgr"
	if _, _, dup := duplicateOpenIntent([]*db.Node{stopped}, again, []int64{4175}); dup {
		t.Fatal("stopped intent must not block a re-issue")
	}
	exhausted := intentNode(3485, "exhausted", "SQL-инъекции на https://lk.cainet.ru: параметры billmgr", 4175)
	if _, _, dup := duplicateOpenIntent([]*db.Node{exhausted}, again, []int64{4175}); dup {
		t.Fatal("exhausted intent must not block a re-issue")
	}
	blocked := intentNode(3486, "blocked", "SQL-инъекции на https://lk.cainet.ru: параметры billmgr", 4175)
	if _, _, dup := duplicateOpenIntent([]*db.Node{blocked}, again, []int64{4175}); dup {
		t.Fatal("blocked intent must not block a re-issue")
	}
}

// Different directions on the same host must stay allowed even with shared anchors.
func TestDuplicateOpenIntentAllowsDifferentDirection(t *testing.T) {
	queued := intentNode(3484, "open",
		"SQL-инъекции на https://lk.cainet.ru: систематически проверить параметры billmgr", 4175, 4015)
	other := "Проверить хранимые и отражённые XSS на https://lk.cainet.ru вне закрытого вектора имени роли: тема и тело тикета, профиль, услуги"
	if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, other, []int64{4175, 4015}); dup {
		t.Fatal("different direction wrongly blocked")
	}
}

// The same wording aimed at a different asset is a different queued direction:
// multi-target batches must not collapse into one intent.
func TestDuplicateOpenIntentAllowsDisjointTargets(t *testing.T) {
	queued := intentNode(1, "open",
		"Verify authentication bypass on https://alpha.example.com", 101)
	again := "Verify authentication bypass on https://bravo.example.com"
	if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, again, []int64{202}); dup {
		t.Fatal("disjoint target sets must not be treated as duplicates")
	}
}

// Two different checks on one host must stay distinct: the shared URL and the
// generic verb are context, not direction.
func TestDuplicateOpenIntentIgnoresSharedTargetContext(t *testing.T) {
	queued := intentNode(1, "open",
		"Test SQL injection on https://app.example.com", 7)
	other := "Test reflected XSS on https://app.example.com"
	if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, other, []int64{7}); dup {
		t.Fatal("different attack vectors on one host must not collapse")
	}
}

// Adding one asset to the same direction must not bypass the check: the queued
// intent already covers the shared target, so the duplicate is rejected and the
// planner is told to split the extra target into its own intent.
func TestDuplicateOpenIntentBlocksExpandedTargetSet(t *testing.T) {
	queued := intentNode(1, "open",
		"Verify authentication bypass on https://alpha.example.com", 101)
	expanded := "Verify authentication bypass on https://alpha.example.com and https://bravo.example.com"
	if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, expanded, []int64{101, 202}); !dup {
		t.Fatal("an expanded target set must still be checked for duplication")
	}
}

// Legacy intents stored target_ids/target_id; the guard must read them too.
func TestDuplicateOpenIntentReadsLegacyTargetKeys(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"summary":    "Verify authentication bypass on https://alpha.example.com",
		"target_ids": []int64{101},
	})
	queued := &db.Node{ID: 1, Kind: db.KindIntent, State: "open", Payload: payload}
	other := "Verify authentication bypass on https://alpha.example.com"
	if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, other, []int64{101}); !dup {
		t.Fatal("legacy target_ids must participate in the duplicate check")
	}
}

// A paused intent is live work that can be resumed; re-issuing the same
// direction would run two copies after resume.
func TestDuplicateOpenIntentBlocksPaused(t *testing.T) {
	paused := intentNode(10, "paused", "Проверить SSRF в вебхуках", 4175)
	if _, _, dup := duplicateOpenIntent([]*db.Node{paused}, "Проверить SSRF в вебхуках", []int64{4175}); !dup {
		t.Fatal("paused intent must block a duplicate")
	}
}

// Short summaries on one asset must not collapse different techniques or
// functions: the anchored floor stays above their similarity.
func TestDuplicateOpenIntentAllowsDistinctTechniquesOnOneAsset(t *testing.T) {
	cases := [][2]string{
		{"Проверить SSRF в вебхуках", "Проверить SSRF в загрузке аватара"},
		{"Проверить SQL-инъекции в billmgr", "Проверить NoSQL-инъекции в billmgr"},
		{"Проверить хранимые XSS в тикетах", "Проверить отражённые XSS в тикетах"},
	}
	for _, c := range cases {
		queued := intentNode(1, "open", c[0], 4175)
		if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, c[1], []int64{4175}); dup {
			t.Errorf("distinct directions wrongly blocked: %q vs %q", c[0], c[1])
		}
	}
}

// CJK summaries have no spaces; without character n-grams a paraphrase is a
// single opaque token and duplicates are never caught.
func TestDuplicateOpenIntentHandlesCJK(t *testing.T) {
	queued := intentNode(1, "open", "测试登录页面的SQL注入", 7)
	if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, "对登录页面做SQL注入测试", []int64{7}); !dup {
		t.Fatal("CJK paraphrase must be recognized as a duplicate")
	}
	other := "测试支付接口的越权访问"
	if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, other, []int64{7}); dup {
		t.Fatal("different CJK directions must not collapse")
	}
}

// Without anchors the host is the only target signal: identical wording about
// different hosts must not be treated as the same direction.
func TestDuplicateOpenIntentUsesHostWhenUnanchored(t *testing.T) {
	queued := intentNode(1, "open", "Test SQL injection on https://a.example.com")
	if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, "Test SQL injection on https://b.example.com", nil); dup {
		t.Fatal("different hosts must not collapse when no anchors are present")
	}
	if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, "Test SQL injection on https://a.example.com", nil); !dup {
		t.Fatal("same host and wording must still be caught without anchors")
	}
}

// Short attack-vector tokens are the direction; dropping them would collapse
// "Test DOM XSS" and "Test RPC RCE" into the same token set.
func TestDuplicateOpenIntentKeepsShortVectorTokens(t *testing.T) {
	queued := intentNode(1, "open", "Test DOM XSS", 7)
	if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, "Test RPC RCE", []int64{7}); dup {
		t.Fatal("different short-vector directions must not collapse")
	}
}

// Shared target asset lowers the similarity floor, so a near-identical wording
// on the same asset is still caught.
func TestDuplicateOpenIntentAnchoredFloor(t *testing.T) {
	queued := intentNode(3486, "open",
		"SSRF на https://lk.cainet.ru: проверить функции, ходящие по URL (проверка домена, загрузка сертификата, вебхуки, интеграции, картинки, long-ссылки, редиректы); цель — внутренние сервисы, метаданные 169.254.169.254, file://, gopher://, доступ к админке на localhost; доказательство — уникальный маркер из ответа внутреннего сервиса.", 4175, 4015)
	again := "Проверить SSRF во всех функциях https://lk.cainet.ru, ходящих по внешнему URL (check_domain, заказ/проверка сертификата, подтверждение домена, вебхуки/интеграции, изображения и аватар, long/{token}, редиректы): подставлять http://127.0.0.1:<порт>, http://localhost, file:///etc/passwd, gopher:// и метаданные, фиксируя уникальный маркер внутреннего ответа; отталкиваться от факта 3433 (внутренний запрос на epp.bmr.cainet.ru, тело не отдаётся) — искать функции, где тело внутреннего ответа возвращается клиенту, не атакуя сторонние сервисы."
	if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, again, []int64{4175, 4015}); !dup {
		t.Fatal("anchored duplicate not blocked")
	}
	// Without the shared asset the same wording stays below the general floor.
	if _, _, dup := duplicateOpenIntent([]*db.Node{queued}, again, []int64{9999}); dup {
		t.Fatal("unanchored pair must not block at this similarity")
	}
}
