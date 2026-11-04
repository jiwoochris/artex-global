// Package deploy holds tests that assert the security posture of the shipped container
// stack. They are ordinary unit tests on purpose: the repository already runs
// `go test ./...` in CI, so a change that silently weakens docker-compose.yml or the
// Dockerfile fails the same gate as a code regression, without adding a toolchain.
package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type composeService struct {
	User          string            `yaml:"user"`
	Privileged    bool              `yaml:"privileged"`
	NetworkMode   string            `yaml:"network_mode"`
	PID           string            `yaml:"pid"`
	IPC           string            `yaml:"ipc"`
	ReadOnly      bool              `yaml:"read_only"`
	CapDrop       []string          `yaml:"cap_drop"`
	CapAdd        []string          `yaml:"cap_add"`
	SecurityOpt   []string          `yaml:"security_opt"`
	PidsLimit     any               `yaml:"pids_limit"`
	Tmpfs         []string          `yaml:"tmpfs"`
	Healthcheck   map[string]any    `yaml:"healthcheck"`
	StopGrace     string            `yaml:"stop_grace_period"`
	Logging       map[string]any    `yaml:"logging"`
	Ports         []any             `yaml:"ports"`
	Volumes       []any             `yaml:"volumes"`
	Environment   map[string]string `yaml:"environment"`
	Networks      []string          `yaml:"networks"`
	DependsOn     map[string]any    `yaml:"depends_on"`
	RestartPolicy string            `yaml:"restart"`
}

type composeNetwork struct {
	Internal bool `yaml:"internal"`
}

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
	Networks map[string]composeNetwork `yaml:"networks"`
	Volumes  map[string]any            `yaml:"volumes"`
}

// Capabilities the product legitimately needs. NET_RAW is what raw-socket port scanning
// requires; the postgres entrypoint needs the other five to chown its data directory and
// drop to the postgres user. Anything else is a finding.
var (
	artexCaps    = map[string]bool{"NET_RAW": true}
	postgresCaps = map[string]bool{"CHOWN": true, "DAC_OVERRIDE": true, "FOWNER": true, "SETGID": true, "SETUID": true}
	// Host paths the container must never see.
	sensitiveMounts = map[string]bool{
		"/var/run/docker.sock": true, "/run/docker.sock": true,
		"/": true, "/etc": true, "/root": true, "/home": true, "/proc": true, "/sys": true,
	}
)

func loadCompose(t *testing.T, path string) composeFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out composeFile
	if err := yaml.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return out
}

// volumeSources returns the host side of every volume entry, skipping named volumes.
func volumeSources(entries []any) []string {
	var sources []string
	for _, entry := range entries {
		switch value := entry.(type) {
		case string:
			parts := strings.Split(value, ":")
			if len(parts) >= 2 {
				sources = append(sources, parts[0])
			}
		case map[string]any:
			if source, ok := value["source"].(string); ok {
				sources = append(sources, source)
			}
		}
	}
	return sources
}

func publishedPort(entry any) string {
	switch value := entry.(type) {
	case string:
		return value
	case map[string]any:
		return strings.TrimSpace(value["published"].(string)) + ":" + value["target"].(string)
	}
	return ""
}

func hasCap(caps []string, want string) bool {
	for _, cap := range caps {
		if strings.EqualFold(cap, want) {
			return true
		}
	}
	return false
}

// defaultUserID resolves the compose `${VAR:-default}` form so the test checks the id that
// is actually used when the operator leaves the variable unset.
func defaultUserID(t *testing.T, user string) string {
	t.Helper()
	// `${ARTEX_UID:-10001}:${ARTEX_GID:-10001}` — take the first group and resolve its default.
	if strings.HasPrefix(user, "${") {
		end := strings.Index(user, "}")
		if end == -1 {
			t.Fatalf("user %q: unterminated variable reference", user)
		}
		expr := user[2:end]
		sep := strings.Index(expr, ":-")
		if sep == -1 {
			t.Fatalf("user %q: a variable default is required so an unset variable cannot mean root", user)
		}
		return expr[sep+2:]
	}
	if sep := strings.Index(user, ":"); sep != -1 {
		return user[:sep]
	}
	return user
}

func onlyCaps(t *testing.T, service, name string, caps []string, allowed map[string]bool) {
	t.Helper()
	for _, cap := range caps {
		if !allowed[strings.ToUpper(cap)] {
			t.Errorf("%s/%s: unexpected capability %q", service, name, cap)
		}
	}
}

// TestComposePosture asserts every isolation setting the stack promises. Each subtest
// names the property, so a failure says which guarantee was lost rather than just
// "config changed".
func TestComposePosture(t *testing.T) {
	root := ".."
	base := loadCompose(t, filepath.Join(root, "docker-compose.yml"))

	artex, ok := base.Services["artex"]
	if !ok {
		t.Fatal("docker-compose.yml: artex service is missing")
	}
	postgres, ok := base.Services["postgres"]
	if !ok {
		t.Fatal("docker-compose.yml: postgres service is missing")
	}

	t.Run("no privileged or host namespaces", func(t *testing.T) {
		for name, service := range base.Services {
			if service.Privileged {
				t.Errorf("%s: privileged must not be enabled", name)
			}
			if service.NetworkMode == "host" {
				t.Errorf("%s: network_mode: host is not allowed (egress goes through app_net)", name)
			}
			if service.PID == "host" || service.IPC == "host" {
				t.Errorf("%s: host pid/ipc namespace is not allowed", name)
			}
		}
	})

	t.Run("no sensitive host mounts", func(t *testing.T) {
		for name, service := range base.Services {
			for _, source := range volumeSources(service.Volumes) {
				if sensitiveMounts[source] || strings.Contains(source, "docker.sock") {
					t.Errorf("%s: sensitive host mount %q", name, source)
				}
			}
		}
	})

	t.Run("no-new-privileges on every service", func(t *testing.T) {
		for name, service := range base.Services {
			joined := strings.Join(service.SecurityOpt, " ")
			if !strings.Contains(joined, "no-new-privileges") {
				t.Errorf("%s: security_opt must contain no-new-privileges", name)
			}
		}
	})

	t.Run("artex runs unprivileged with a single capability", func(t *testing.T) {
		if artex.User == "" {
			t.Error("artex: user must be pinned")
		}
		if uid := defaultUserID(t, artex.User); uid == "0" || strings.EqualFold(uid, "root") {
			t.Errorf("artex: default user resolves to root (%q)", artex.User)
		}
		if !hasCap(artex.CapDrop, "ALL") {
			t.Errorf("artex: cap_drop must contain ALL, got %v", artex.CapDrop)
		}
		onlyCaps(t, "artex", "cap_add", artex.CapAdd, artexCaps)
	})

	t.Run("resource, log and lifecycle caps", func(t *testing.T) {
		if artex.PidsLimit == nil {
			t.Error("artex: pids_limit is required (fork-bomb guard)")
		}
		if artex.Logging["driver"] != "json-file" {
			t.Errorf("artex: logging driver must be json-file, got %v", artex.Logging["driver"])
		}
		options, _ := artex.Logging["options"].(map[string]any)
		if options == nil || options["max-size"] == nil {
			t.Error("artex: logging rotation (options.max-size) is required")
		}
		if artex.Healthcheck == nil {
			t.Error("artex: healthcheck is required")
		}
		if artex.StopGrace == "" {
			t.Error("artex: stop_grace_period is required so docker stop does not kill running work")
		}
		if len(artex.Tmpfs) == 0 {
			t.Error("artex: a capped /tmp tmpfs is required")
		} else if !strings.Contains(strings.Join(artex.Tmpfs, " "), "size=") {
			t.Errorf("artex: /tmp tmpfs must be size-capped, got %v", artex.Tmpfs)
		}
	})

	t.Run("ports bind loopback only", func(t *testing.T) {
		for _, entry := range artex.Ports {
			published := publishedPort(entry)
			host := strings.Split(published, ":")[0]
			if host == "" || host == "0" || host == "0.0.0.0" {
				t.Errorf("artex: port %q must bind 127.0.0.1 by default", published)
			}
		}
	})

	t.Run("postgres is internal and unpublished", func(t *testing.T) {
		if len(postgres.Ports) != 0 {
			t.Errorf("postgres: must not publish a port, got %v", postgres.Ports)
		}
		if len(postgres.Networks) == 0 {
			t.Fatal("postgres: must join an internal network")
		}
		for _, name := range postgres.Networks {
			if !base.Networks[name].Internal {
				t.Errorf("postgres: network %q must be internal (no egress)", name)
			}
		}
		if !hasCap(postgres.CapDrop, "ALL") {
			t.Errorf("postgres: cap_drop must contain ALL, got %v", postgres.CapDrop)
		}
		onlyCaps(t, "postgres", "cap_add", postgres.CapAdd, postgresCaps)
	})
}

// TestComposeOverlays keeps the two opt-in overlays honest: the volumes overlay must
// replace every host bind mount with a named volume (otherwise it does not remove the
// chown requirement), and the read-only overlay must actually freeze the rootfs and make
// HOME writable.
func TestComposeOverlays(t *testing.T) {
	volumes := loadCompose(t, filepath.Join("..", "docker-compose.volumes.yml"))
	overlayArtex, ok := volumes.Services["artex"]
	if !ok {
		t.Fatal("docker-compose.volumes.yml: artex service is missing")
	}
	for _, source := range volumeSources(overlayArtex.Volumes) {
		if strings.HasPrefix(source, ".") || strings.HasPrefix(source, "/") {
			t.Errorf("volumes overlay: %q is still a host path", source)
		}
	}
	if len(volumes.Volumes) == 0 {
		t.Error("volumes overlay: must declare the named volumes it uses")
	}

	readonly := loadCompose(t, filepath.Join("..", "docker-compose.readonly.yml"))
	roArtex, ok := readonly.Services["artex"]
	if !ok {
		t.Fatal("docker-compose.readonly.yml: artex service is missing")
	}
	if !roArtex.ReadOnly {
		t.Error("readonly overlay: must set read_only: true")
	}
	if roArtex.Environment["HOME"] == "" {
		t.Error("readonly overlay: must point HOME at a writable path (Chromium/pnpm need it)")
	}

	// The volumes overlay is seeded from the image, so it must keep the image uid: honouring
	// the ARTEX_UID bind-mount override there would make the volume unwritable.
	if got := defaultUserID(t, overlayArtex.User); got != "10001" {
		t.Errorf("volumes overlay: user must pin the image uid 10001, got %q", overlayArtex.User)
	}
}

// TestDockerfilePosture covers the invariants that live in the image itself, where a
// compose-level check cannot see them.
func TestDockerfilePosture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	text := string(raw)

	t.Run("runtime user is unprivileged", func(t *testing.T) {
		if !strings.Contains(text, "USER ${ARTEX_UID}:${ARTEX_GID}") {
			t.Error("Dockerfile: runtime USER must be the unprivileged artex uid/gid")
		}
		if strings.Contains(text, "USER root") {
			t.Error("Dockerfile: must not switch to root at runtime")
		}
	})

	t.Run("base image is digest-pinned", func(t *testing.T) {
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "FROM ") && !strings.Contains(line, "@sha256:") {
				t.Errorf("Dockerfile: base image is not digest-pinned: %s", line)
			}
		}
	})

	t.Run("stopsignal and healthcheck are declared", func(t *testing.T) {
		if !strings.Contains(text, "STOPSIGNAL SIGTERM") {
			t.Error("Dockerfile: STOPSIGNAL SIGTERM is required (start.sh forwards it to artex)")
		}
		if !strings.Contains(text, "HEALTHCHECK") {
			t.Error("Dockerfile: a HEALTHCHECK is required so `docker run` without compose reports health")
		}
	})

	t.Run("node is installed from a checksummed tarball", func(t *testing.T) {
		// Comments are allowed to name the rejected remote installer; only real
		// instructions matter.
		for _, line := range strings.Split(text, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if strings.Contains(trimmed, "nodesource.com") || strings.Contains(trimmed, "setup_22.x") {
				t.Errorf("Dockerfile: Node must come from the pinned tarball with a SHA256 check, got %q", trimmed)
			}
		}
		if !strings.Contains(text, "sha256sum -c -") {
			t.Error("Dockerfile: the Node tarball checksum must be verified")
		}
	})
}

// TestNoLegacyHardenedStack guards the consolidation: the second stack and its documents
// were removed on purpose, and reintroducing a `*-hardened` variant would split the
// posture again.
func TestNoLegacyHardenedStack(t *testing.T) {
	for _, path := range []string{"Dockerfile.hardened", "docker-compose.hardened.yml", "docs/docker-hardened.md"} {
		if _, err := os.Stat(filepath.Join("..", path)); err == nil {
			t.Errorf("%s exists again: the stack was consolidated into docker-compose.yml + docs/docker.md", path)
		}
	}
}
