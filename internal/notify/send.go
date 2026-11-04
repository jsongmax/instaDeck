package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"instadeck/internal/netguard"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"syscall"
	"time"
)

var blocked = errors.New("address blocked")

func (s *Service) send(ctx context.Context, c Channel, m Message) error {
	config, err := s.config(c)
	if err != nil {
		return &Failure{Code: "notify_unreachable"}
	}
	endpoint := config.URL
	var body []byte
	if c.Type == "telegram" {
		base := "https://api.telegram.org"
		if s.telegramBase != "" {
			base = s.telegramBase
		}
		endpoint = base + "/bot" + config.BotToken + "/sendMessage"
		body, _ = json.Marshal(map[string]string{"chat_id": config.ChatID, "text": m.Title + "\n" + m.Message + "\n账号: " + m.Account + "\n机器: " + m.Machine})
	} else {
		body, _ = json.Marshal(m)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return &Failure{Code: "notify_unreachable"}
	}
	request.Header.Set("Content-Type", "application/json")
	if c.Type == "webhook" && config.SigningKey != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(config.SigningKey))
		mac.Write([]byte(timestamp + "."))
		mac.Write(body)
		request.Header.Set("X-InstaDeck-Timestamp", timestamp)
		request.Header.Set("X-InstaDeck-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	dialer := net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return blocked
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || s.block.Load() && !netguard.SafeAddress(ip, false) {
			return blocked
		}
		return nil
	}}
	transport := &http.Transport{TLSClientConfig: s.tlsConfig, DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, blocked) {
			return &Failure{Code: "notify_address_blocked"}
		}
		return &Failure{Code: "notify_unreachable"}
	}
	defer response.Body.Close() // Deliberately do not read or log response bodies.
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	f := &Failure{Code: "notify_rejected"}
	if response.StatusCode == 401 || response.StatusCode == 403 {
		f.Code = "notify_unauthorized"
	}
	if response.StatusCode == 429 {
		if seconds, e := strconv.Atoi(response.Header.Get("Retry-After")); e == nil && seconds >= 0 {
			f.RetryAfter = time.Duration(min(seconds, 604800)) * time.Second
		} else if until, e := http.ParseTime(response.Header.Get("Retry-After")); e == nil {
			f.RetryAfter = time.Until(until)
		}
	}
	return f
}
