package oob

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Fatal("OOB 必须默认关闭：这是外部服务流量")
	}
	if cfg.ServerURL != DefaultServerURL {
		t.Fatalf("默认服务应为 %s, got %q", DefaultServerURL, cfg.ServerURL)
	}
	if cfg.Token != "" {
		t.Fatal("默认公共服务无需 token")
	}
}

func TestPollRejectsUndecodablePayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/register":
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "registration successful"})
		case "/poll":
			_ = json.NewEncoder(w).Encode(pollResponse{Data: []string{"garbage"}, AESKey: "not-a-key"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, err := newTestSession(t, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = s.poll(func(Interaction) {})
	if !errors.Is(err, errPayloadUndecodable) {
		t.Fatalf("undecodable payload must be reported, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(Config{Enabled: false}); err != nil {
		t.Fatalf("禁用时不应报错: %v", err)
	}
	if err := Validate(Config{Enabled: true, ServerURL: "oast.example"}); err != nil {
		t.Fatalf("裸主机名应合法: %v", err)
	}
	if err := Validate(Config{Enabled: true, ServerURL: ""}); err == nil {
		t.Fatal("启用 OOB 但无服务地址应明确报错")
	}
	if err := Validate(Config{Enabled: true, ServerURL: "http://127.0.0.1:8443"}); err == nil {
		t.Fatal("启用 OOB 但使用明文 HTTP 应明确报错")
	}
}

// encryptFor encrypts plaintext exactly the way the Interactsh server does:
// a random AES key encrypted to the client's RSA public key with OAEP/SHA-256,
// and the payload encrypted with AES-CTR using a 16-byte IV prefix.
func encryptFor(t *testing.T, pub *rsa.PublicKey, plaintext []byte) (string, string) {
	t.Helper()
	aesKey := make([]byte, 32)
	if _, err := rand.Read(aesKey); err != nil {
		t.Fatal(err)
	}
	encKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, aesKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	ct := make([]byte, aes.BlockSize+len(plaintext))
	copy(ct, iv)
	cipher.NewCTR(block, iv).XORKeyStream(ct[aes.BlockSize:], plaintext)
	return base64.StdEncoding.EncodeToString(encKey), base64.StdEncoding.EncodeToString(ct)
}

// fakeServer implements /register, /poll and /deregister well enough to prove
// the client speaks the public protocol and decrypts callbacks.
func fakeServer(t *testing.T) (*httptest.Server, func() *rsa.PublicKey, *atomic.Bool) {
	t.Helper()
	var mu sync.Mutex
	var pub *rsa.PublicKey
	var deregistered atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/register":
			var req registerRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			raw, err := base64.StdEncoding.DecodeString(req.PublicKey)
			if err != nil {
				http.Error(w, "bad public key", http.StatusBadRequest)
				return
			}
			block, _ := pem.Decode(raw)
			if block == nil {
				http.Error(w, "bad PEM", http.StatusBadRequest)
				return
			}
			parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			rsaPub, ok := parsed.(*rsa.PublicKey)
			if !ok {
				http.Error(w, "not an RSA key", http.StatusBadRequest)
				return
			}
			mu.Lock()
			pub = rsaPub
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "registration successful"})
		case "/poll":
			mu.Lock()
			p := pub
			mu.Unlock()
			if p == nil {
				http.Error(w, "no registration", http.StatusBadRequest)
				return
			}
			payload, _ := json.Marshal(Interaction{
				Protocol:  "dns",
				UniqueID:  "probe",
				FullID:    "probe.oast.example",
				Remote:    "203.0.113.7",
				Timestamp: time.Now().UTC(),
			})
			key, data := encryptFor(t, p, payload)
			_ = json.NewEncoder(w).Encode(pollResponse{Data: []string{data}, AESKey: key})
		case "/deregister":
			deregistered.Store(true)
			_, _ = w.Write([]byte(`{"message":"deregistered"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() *rsa.PublicKey {
		mu.Lock()
		defer mu.Unlock()
		return pub
	}, &deregistered
}

// newTestSession builds a session without Validate (httptest serves plaintext
// HTTP; the cleartext ban is covered by TestValidate) and registers it.
func newTestSession(t *testing.T, serverURL string) (*Session, error) {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{
		serverURL:     u,
		privKey:       priv,
		correlationID: randomString(correlationIDLength),
		secretKey:     newUUID(),
		health:        &healthState{},
		quit:          make(chan struct{}),
		http:          newHTTPClient(Config{}),
	}
	if err := s.register(); err != nil {
		return nil, err
	}
	return s, nil
}

func TestSessionRegistersMintsAndDecrypts(t *testing.T) {
	srv, pubFn, deregistered := fakeServer(t)
	s, err := newTestSession(t, srv.URL)
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if s.correlationID == "" || s.secretKey == "" {
		t.Fatal("会话缺少 correlation id 或 secret key")
	}
	host1, host2 := s.URL(), s.URL()
	if host1 == "" || host2 == "" || host1 == host2 {
		t.Fatalf("URL 应每次铸造不同的探测地址: %q %q", host1, host2)
	}
	if got, want := host1[:len(s.correlationID)], s.correlationID; got != want {
		t.Fatalf("探测地址应以 correlation id 开头: got %q want %q", got, want)
	}

	var got []Interaction
	if err := s.poll(func(in Interaction) { got = append(got, in) }); err != nil {
		t.Fatalf("轮询失败: %v", err)
	}
	if len(got) != 1 || got[0].Protocol != "dns" || got[0].Remote != "203.0.113.7" {
		t.Fatalf("回调未正确解密: %+v", got)
	}
	if pubFn() == nil {
		t.Fatal("服务端未收到公钥")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if !deregistered.Load() {
		t.Fatal("Close 应注销会话")
	}
	if s.URL() != "" {
		t.Fatal("关闭后不应再铸造地址")
	}
}

func TestSessionRegisterFailureIsLoud(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"nope"}`))
	}))
	defer srv.Close()
	if _, err := newTestSession(t, srv.URL); err == nil {
		t.Fatal("注册失败必须明确报错，不得静默回退")
	}
}

func TestSessionUnreachableServer(t *testing.T) {
	if _, err := NewClient(Config{Enabled: true, ServerURL: "https://127.0.0.1:1"}); err == nil {
		t.Fatal("连接不可达服务应明确失败")
	}
}

func TestHTTPClientHasBoundedIdleTimeout(t *testing.T) {
	s := &Session{http: newHTTPClient(Config{})}
	tr, ok := s.http.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected a dedicated HTTP transport")
	}
	if tr.IdleConnTimeout <= 0 {
		t.Fatalf("idle connections must have a bounded lifetime, got %v", tr.IdleConnTimeout)
	}
}
