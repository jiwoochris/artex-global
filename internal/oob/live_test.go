package oob

import (
	"net"
	"os"
	"testing"
	"time"
)

func TestLiveOastMe(t *testing.T) {
	if os.Getenv("OOB_LIVE") != "1" {
		t.Skip("set OOB_LIVE=1 to run against the public service")
	}
	s, err := NewClient(Config{Enabled: true, ServerURL: "https://oast.me"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer s.Close()
	host := s.URL()
	t.Logf("host=%s", host)
	if _, err := net.LookupHost(host); err != nil {
		t.Logf("lookup: %v", err)
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		var got []Interaction
		if err := s.poll(func(in Interaction) { got = append(got, in) }); err != nil {
			t.Fatalf("poll: %v", err)
		}
		if len(got) > 0 {
			t.Logf("CALLBACK protocol=%s full=%q remote=%s", got[0].Protocol, got[0].FullID, got[0].Remote)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("no callback observed")
}
