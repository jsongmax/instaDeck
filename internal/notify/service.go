package notify

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"instadeck/internal/keyring"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxRetryDelay caps Retry-After. One worker serves every channel, so an endpoint
// must not be able to stall all notifications for as long as it asks.
const maxRetryDelay = time.Minute

type job struct{ message Message }
type Service struct {
	repo           Repository
	ring           *keyring.Ring
	logger         *slog.Logger
	queue          chan job
	changed        chan struct{}
	block          atomic.Bool
	cancel         context.CancelFunc
	workers        sync.WaitGroup
	tlsConfig      *tls.Config
	telegramBase   string
	ResolveAccount func(string) string
}

func New(repo Repository, ring *keyring.Ring, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{repo: repo, ring: ring, logger: logger, queue: make(chan job, 64), changed: make(chan struct{}, 1)}
	s.block.Store(true)
	return s
}
func (s *Service) SetAddressFilter(v bool) { s.block.Store(v) }
func (s *Service) config(c Channel) (Config, error) {
	raw, err := s.ring.Decrypt(c.Envelope, AAD(c.ID))
	if err != nil {
		return Config{}, &Failure{Code: "unavailable"}
	}
	defer clear(raw)
	var v Config
	if json.Unmarshal(raw, &v) != nil {
		return v, &Failure{Code: "unavailable"}
	}
	return v, nil
}
func (s *Service) List(ctx context.Context) ([]Channel, error) {
	values, err := s.repo.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range values {
		v, e := s.config(values[i])
		if e != nil {
			return nil, e
		}
		if values[i].Type == "telegram" {
			values[i].Hint = "Chat ID: " + v.ChatID
		} else {
			u, _ := url.Parse(v.URL)
			if u != nil {
				values[i].Hint = u.Hostname()
			}
		}
	}
	return values, nil
}
func (s *Service) Save(ctx context.Context, c Channel, input Config, create bool) (Channel, error) {
	if create {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return c, &Failure{Code: "unavailable"}
		}
		c.ID = hex.EncodeToString(id[:])
	} else {
		values, err := s.repo.ListChannels(ctx)
		if err != nil {
			return c, err
		}
		found := false
		for _, old := range values {
			if old.ID == c.ID {
				found = true
				if c.Type != old.Type {
					return c, &Failure{Code: "invalid_request"}
				}
				v, err := s.config(old)
				if err != nil {
					return c, err
				}
				if input.BotToken == "" {
					input.BotToken = v.BotToken
				}
				if input.ChatID == "" {
					input.ChatID = v.ChatID
				}
				if input.URL == "" {
					input.URL = v.URL
				}
				if input.SigningKey == "" {
					input.SigningKey = v.SigningKey
				}
			}
		}
		if !found {
			return c, &Failure{Code: "not_found"}
		}
	}
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 128 || len(input.BotToken) > 512 || len(input.ChatID) > 128 || len(input.URL) > 4096 || len(input.SigningKey) > 4096 {
		return c, &Failure{Code: "invalid_request"}
	}
	for _, event := range c.Events {
		if !slices.Contains(Events, event) {
			return c, &Failure{Code: "invalid_request"}
		}
	}
	if c.Type == "telegram" {
		if input.BotToken == "" || input.ChatID == "" || strings.ContainsAny(input.BotToken, "/ ?#\r\n") {
			return c, &Failure{Code: "invalid_request"}
		}
	} else if c.Type == "webhook" {
		u, err := url.Parse(input.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return c, &Failure{Code: "invalid_request"}
		}
	} else {
		return c, &Failure{Code: "invalid_request"}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return c, &Failure{Code: "invalid_request"}
	}
	defer clear(raw)
	c.Envelope, err = s.ring.Encrypt(raw, AAD(c.ID))
	if err != nil {
		return c, &Failure{Code: "unavailable"}
	}
	err = s.repo.SaveChannel(ctx, c, create)
	if err == nil {
		s.signalChange()
	}
	return c, err
}
func (s *Service) signalChange() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}
func (s *Service) Delete(ctx context.Context, id string) error {
	err := s.repo.DeleteChannel(ctx, id)
	if err == nil {
		s.signalChange()
	}
	return err
}
func (s *Service) Test(ctx context.Context, id string) error {
	values, err := s.repo.ListChannels(ctx)
	if err != nil {
		return &Failure{Code: "unavailable"}
	}
	for _, c := range values {
		if c.ID == id {
			return s.send(ctx, c, Message{Event: "test", Title: "InstaDeck 测试通知", Message: "通知渠道配置成功", Time: time.Now().UTC()})
		}
	}
	return &Failure{Code: "not_found"}
}

var privateText = regexp.MustCompile(`(?i)(?:[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}|(?:ibk_|ibp_|sk-)[a-z0-9_\-]+|[0-9]{6,}:[a-z0-9_\-]+|-----BEGIN[^\n]*)`)

func safeText(v string) string {
	if strings.Contains(v, "-----BEGIN") {
		return "[已隐藏]"
	}
	v = privateText.ReplaceAllString(v, "[已隐藏]")
	r := []rune(v)
	if len(r) > 200 {
		v = string(r[:200])
	}
	return v
}

// Emit only enqueues; no network or database work occurs on the business path.
func (s *Service) Emit(m Message) {
	m.Title = safeText(m.Title)
	m.Message = safeText(m.Message)
	m.Machine = safeText(m.Machine)
	m.Account = safeText(m.Account)
	if m.Time.IsZero() {
		m.Time = time.Now().UTC()
	}
	select {
	case s.queue <- job{message: m}:
	default:
		s.logger.Warn("notification dropped", "code", "queue_full")
	}
}
func (s *Service) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-s.queue:
				if s.ResolveAccount != nil && j.message.Event == "operation.failed" {
					j.message.Account = safeText(s.ResolveAccount(j.message.Account))
				}
				values, err := s.repo.ListChannels(ctx)
				if err != nil {
					s.logger.Warn("notification failed", "code", "storage_unavailable")
					continue
				}
				for _, c := range values {
					if !c.Enabled || !slices.Contains(c.Events, j.message.Event) {
						continue
					}
					for attempt := 0; attempt < 3; attempt++ {
						err = s.send(ctx, c, j.message)
						if err == nil {
							break
						}
						var f *Failure
						code := "notify_unreachable"
						delay := time.Second << attempt
						if errors.As(err, &f) {
							code = f.Code
							if f.RetryAfter > delay {
								delay = min(f.RetryAfter, maxRetryDelay)
							}
						}
						s.logger.Warn("notification failed", "code", code)
						if attempt == 2 || code == "notify_unauthorized" || code == "notify_address_blocked" {
							break
						}
						timer := time.NewTimer(delay)
						select {
						case <-ctx.Done():
							timer.Stop()
							return
						case <-timer.C:
						}
					}
				}
			}
		}
	}()
}
func (s *Service) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.workers.Wait()
}
