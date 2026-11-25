package db

import (
	"regexp"
	"strings"
	"testing"
)

func TestRMGuardPatterns(t *testing.T) {
	recursive, critical := rmGuardPatterns()
	reRec := regexp.MustCompile(recursive)
	reCrit := regexp.MustCompile(critical)

	blocked := []string{
		`rm -rf "/etc"`,
		`rm -rf '/etc'`,
		"rm -rf /*",
		"rm -rf /app/data",
		"rm -rf /app/data/*",
		"rm -rf /app/data&&true",
		"rm -rf /etc; true",
		`rm -rf "/tmp/first" /etc`,
		"rm -rf data",
		"rm -rf ./data",
		"rm --recursive /etc",
	}
	for _, cmd := range blocked {
		if !reRec.MatchString(cmd) {
			t.Errorf("recursive guard must block %q", cmd)
		}
	}
	criticalBlocked := []string{
		`rm -rf "/etc"`,
		"rm -rf /*",
		"rm -rf /app/data/*",
		`rm -rf "/tmp/first" /etc`,
		"rm -rf /etc; true",
		"rm -rf data",
	}
	for _, cmd := range criticalBlocked {
		if !reCrit.MatchString(cmd) {
			t.Errorf("critical guard must block %q", cmd)
		}
	}

	allowed := []string{
		"rm -rf /app/data/tasks/58/tmp",
		"rm -rf /app/data/tasks/58/tmp/",
		"rm -rf data/tasks/58/tmp",
		"rm -rf /tmp/foo",
	}
	for _, cmd := range allowed {
		if reRec.MatchString(cmd) {
			t.Errorf("recursive guard must allow task cleanup %q", cmd)
		}
		if reCrit.MatchString(cmd) {
			t.Errorf("critical guard must allow task cleanup %q", cmd)
		}
	}
}

func TestDataRootAlternativesIncludeExecutableDataDir(t *testing.T) {
	alts := dataRootAlternatives()
	if !strings.Contains(alts, "/app/data") {
		t.Fatalf("Docker data root missing: %s", alts)
	}
	if !strings.Contains(alts, "/data") {
		t.Fatalf("executable-adjacent data root missing: %s", alts)
	}
}

func TestBuiltInToolScopesIncludeAliases(t *testing.T) {
	command := strings.Join(commandToolNames(), " ")
	for _, alias := range []string{"Bash", "bash", "BASH", "shell_open", "shell_send"} {
		if !strings.Contains(command, alias) {
			t.Errorf("command scope missing alias %q: %s", alias, command)
		}
	}
	http := strings.Join(httpToolNames(), " ")
	for _, alias := range []string{"WebFetch", "web_fetch", "WEBFETCH", "ExecuteExtraTool", "execute_extra_tool", "EXECUTEEXTRATOOL"} {
		if !strings.Contains(http, alias) {
			t.Errorf("http scope missing alias %q: %s", alias, http)
		}
	}
}

func TestHTTPBuiltInClassification(t *testing.T) {
	for _, name := range []string{"[内置] 删除类接口路径", "[内置] 批量清空 / 清除接口路径"} {
		if !isHTTPBuiltIn(name) {
			t.Errorf("%q must be scoped to HTTP tools", name)
		}
	}
	for _, name := range []string{"[内置] 数据外泄管道", "[内置] 递归强制删除 rm -rf"} {
		if isHTTPBuiltIn(name) {
			t.Errorf("%q must stay a command rule", name)
		}
	}
}
