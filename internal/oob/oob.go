// Package oob implements a minimal Interactsh client on the standard library:
// registration, callback-address minting, polling, and payload decryption.
// It speaks the public server protocol (POST /register, GET /poll,
// POST /deregister) and keeps the RSA private key in memory only — it is never
// persisted to the DB, logs, or tool results.
package oob

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultServerURL is the public Interactsh instance used out of the box.
// The public service is a shared multi-tenant deployment: its operator can see
// callback metadata and payload content, so an operator may point server_url at
// a self-hosted instance instead.
const DefaultServerURL = "https://oast.me"

const (
	correlationIDLength = 20
	nonceLength         = 13
	requestTimeout      = 10 * time.Second
)

// zbase32Alphabet is the lowercase alphabet the official client uses for the
// per-probe nonce. It is DNS-label safe.
const zbase32Alphabet = "ybndrfg8ejkmcpqxot1uwisza345h769"

// Config is the resolved OOB configuration. It comes from the on-disk config
// file (config.json "oob" block) with sane defaults: enabled, public service.
// A self-hosted address is optional; a token is only used if set.
type Config struct {
	Enabled   bool   `json:"enabled"`
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
}

// DefaultConfig returns the shipped defaults: enabled against the public
// Interactsh service at oast.me.
func DefaultConfig() Config {
	return Config{Enabled: true, ServerURL: DefaultServerURL}
}

// Validate checks a Config for internal consistency. An enabled config with no
// server URL, or with a cleartext HTTP endpoint, is an error (fail loud, no
// silent fallback to another service or transport).
func Validate(c Config) error {
	if !c.Enabled {
		return nil
	}
	raw := strings.TrimSpace(c.ServerURL)
	if raw == "" {
		return errors.New("oob: 已启用但 server_url 为空；请配置服务地址或删除 oob 配置以使用默认公共服务")
	}
	if strings.HasPrefix(strings.ToLower(raw), "http://") {
		return fmt.Errorf("oob: 不允许明文 HTTP 服务地址 %q；请使用 HTTPS 或仅填写主机名", raw)
	}
	return nil
}

// Interaction is the safe metadata subset of an Interactsh callback.
type Interaction struct {
	Protocol  string    `json:"protocol"`
	UniqueID  string    `json:"unique-id"`
	FullID    string    `json:"full-id"`
	QType     string    `json:"q-type,omitempty"`
	Raw       string    `json:"raw-request,omitempty"`
	SMTPFrom  string    `json:"smtp-from,omitempty"`
	Remote    string    `json:"remote-address"`
	Timestamp time.Time `json:"timestamp"`
}

type registerRequest struct {
	PublicKey     string `json:"public-key"`
	SecretKey     string `json:"secret-key"`
	CorrelationID string `json:"correlation-id"`
}

type registerResponse struct {
	Message string `json:"message"`
}

type deregisterRequest struct {
	CorrelationID string `json:"correlation-id"`
	SecretKey     string `json:"secret-key"`
}

type pollResponse struct {
	Data    []string `json:"data"`
	Extra   []string `json:"extra"`
	AESKey  string   `json:"aes_key"`
	TLDData []string `json:"tlddata,omitempty"`
}

// healthState records poll failures. Without it an outage would be
// indistinguishable from a healthy empty window; the agent reads this snapshot
// after each poll.
type healthState struct {
	mu     sync.Mutex
	n      int64
	err    error
	lastAt time.Time
	lastOK time.Time
}

func (s *healthState) record(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.n++
	s.err = err
	s.lastAt = time.Now()
	s.mu.Unlock()
}

func (s *healthState) recordOK() {
	s.mu.Lock()
	s.lastOK = time.Now()
	s.err = nil
	s.mu.Unlock()
}

func (s *healthState) snapshot() (int64, error, time.Time, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n, s.err, s.lastAt, s.lastOK
}

// Session is one live Interactsh session scoped to a single worker run.
type Session struct {
	serverURL     *url.URL
	token         string
	privKey       *rsa.PrivateKey
	correlationID string
	secretKey     string
	http          *http.Client
	health        *healthState

	quit     chan struct{}
	quitOnce sync.Once
	pollWG   sync.WaitGroup
	mu       sync.Mutex
	closed   bool
}

// NewClient builds a live Interactsh session: it generates an RSA key pair and
// registers with the server. The private key stays inside the session.
func NewClient(c Config) (*Session, error) {
	if err := Validate(c); err != nil {
		return nil, err
	}
	serverURL, err := parseServerURL(c.ServerURL)
	if err != nil {
		return nil, err
	}
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("oob: 生成 RSA 密钥失败: %w", err)
	}
	s := &Session{
		serverURL:     serverURL,
		token:         strings.TrimSpace(c.Token),
		privKey:       priv,
		correlationID: randomString(correlationIDLength),
		secretKey:     newUUID(),
		health:        &healthState{},
		quit:          make(chan struct{}),
		http:          newHTTPClient(),
	}
	if err := s.register(); err != nil {
		return nil, err
	}
	return s, nil
}

func parseServerURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("oob: 服务地址为空")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("oob: 服务地址无效: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("oob: 服务地址缺少主机名: %q", raw)
	}
	return u, nil
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
}

func (s *Session) register() error {
	pub, err := encodePublicKey(&s.privKey.PublicKey)
	if err != nil {
		return err
	}
	body, err := json.Marshal(registerRequest{
		PublicKey:     pub,
		SecretKey:     s.secretKey,
		CorrelationID: s.correlationID,
	})
	if err != nil {
		return fmt.Errorf("oob: 构造注册请求失败: %w", err)
	}
	var resp registerResponse
	if err := s.postJSON("/register", body, &resp); err != nil {
		return err
	}
	if resp.Message != "registration successful" {
		return fmt.Errorf("oob: 注册失败: %s", resp.Message)
	}
	return nil
}

// URL returns a fresh callback hostname: correlation ID + random nonce + the
// server's host. Each call mints a unique probe address valid for this session.
func (s *Session) URL() string {
	if s == nil || s.isClosed() {
		return ""
	}
	return s.correlationID + randomString(nonceLength) + "." + s.serverURL.Host
}

// StartPolling polls the server on the given interval until StopPolling is
// called, delivering every interaction to cb exactly as received.
func (s *Session) StartPolling(interval time.Duration, cb func(Interaction)) error {
	if s == nil || s.isClosed() {
		return errors.New("oob: 会话未初始化")
	}
	if interval <= 0 {
		interval = 3 * time.Second
	}
	s.pollWG.Add(1)
	go func() {
		defer s.pollWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.quit:
				return
			case <-ticker.C:
				if err := s.poll(cb); err != nil {
					s.health.record(err)
				} else {
					s.health.recordOK()
				}
			}
		}
	}()
	return nil
}

func (s *Session) poll(cb func(Interaction)) error {
	var resp pollResponse
	if err := s.getJSON("/poll?id="+url.QueryEscape(s.correlationID)+"&secret="+url.QueryEscape(s.secretKey), &resp); err != nil {
		return err
	}
	for _, data := range resp.Data {
		plaintext, err := s.decryptMessage(resp.AESKey, data)
		if err != nil {
			continue
		}
		var in Interaction
		if err := json.Unmarshal(bytes.TrimRight(plaintext, " \t\r\n"), &in); err != nil {
			continue
		}
		cb(in)
	}
	for _, raw := range append(append([]string{}, resp.Extra...), resp.TLDData...) {
		var in Interaction
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			continue
		}
		cb(in)
	}
	return nil
}

// decryptMessage decrypts one AES-CTR message whose key was encrypted to this
// session's RSA public key with OAEP/SHA-256.
func (s *Session) decryptMessage(key, secureMessage string) ([]byte, error) {
	if key == "" {
		return nil, errors.New("oob: 轮询响应缺少 aes_key")
	}
	decodedKey, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return nil, fmt.Errorf("oob: 解码 AES 密钥失败: %w", err)
	}
	aesKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, s.privKey, decodedKey, nil)
	if err != nil {
		return nil, fmt.Errorf("oob: 解密 AES 密钥失败: %w", err)
	}
	cipherText, err := base64.StdEncoding.DecodeString(secureMessage)
	if err != nil {
		return nil, fmt.Errorf("oob: 解码回调数据失败: %w", err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, fmt.Errorf("oob: 初始化 AES 失败: %w", err)
	}
	if len(cipherText) < aes.BlockSize {
		return nil, errors.New("oob: 回调数据过短")
	}
	iv := cipherText[:aes.BlockSize]
	cipherText = cipherText[aes.BlockSize:]
	stream := cipher.NewCTR(block, iv)
	decoded := make([]byte, len(cipherText))
	stream.XORKeyStream(decoded, cipherText)
	return decoded, nil
}

// Health returns the cumulative poll-failure count, latest error, when that
// failure occurred, and when the last successful poll completed.
func (s *Session) Health() (int64, error, time.Time, time.Time) {
	if s == nil {
		return 0, errors.New("oob: 会话未初始化"), time.Time{}, time.Time{}
	}
	return s.health.snapshot()
}

// StopPolling halts the polling goroutine. It must be called before Close.
func (s *Session) StopPolling() error {
	if s == nil {
		return nil
	}
	s.quitOnce.Do(func() { close(s.quit) })
	s.pollWG.Wait()
	return nil
}

// Close stops polling and deregisters from the server. The private key is
// discarded with the session.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	if err := s.StopPolling(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	body, err := json.Marshal(deregisterRequest{CorrelationID: s.correlationID, SecretKey: s.secretKey})
	if err != nil {
		return err
	}
	var out registerResponse
	// deregistration is best-effort: the server expires the session anyway
	_ = s.postJSON("/deregister", body, &out)
	return nil
}

func (s *Session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Session) postJSON(path string, body []byte, out any) error {
	req, err := http.NewRequest(http.MethodPost, s.serverURL.String()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", s.token)
	}
	return s.do(req, out)
}

func (s *Session) getJSON(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, s.serverURL.String()+path, nil)
	if err != nil {
		return err
	}
	if s.token != "" {
		req.Header.Set("Authorization", s.token)
	}
	return s.do(req, out)
}

func (s *Session) do(req *http.Request, out any) error {
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("oob: 服务拒绝了 token")
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("oob: 服务返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func encodePublicKey(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("oob: 编码公钥失败: %w", err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: der})
	return base64.StdEncoding.EncodeToString(block), nil
}

// randomString returns n characters drawn from the zbase32 alphabet.
func randomString(n int) string {
	out := make([]byte, n)
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is not recoverable here; the session is unusable
		// without a unique correlation id, so panic rather than reuse a value.
		panic("oob: crypto/rand failed: " + err.Error())
	}
	for i, b := range buf {
		out[i] = zbase32Alphabet[int(b)%len(zbase32Alphabet)]
	}
	return string(out)
}

// newUUID returns a random RFC 4122 v4 UUID string.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("oob: crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
