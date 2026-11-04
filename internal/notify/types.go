// Package notify delivers bounded, asynchronous notifications without logging secrets.
package notify

import (
	"context"
	"instadeck/internal/keyring"
	"time"
)

var Events = []string{"credential.invalid", "operation.failed", "machine.error", "machine.stopped"}

type Config struct {
	BotToken   string `json:"botToken"`
	ChatID     string `json:"chatId"`
	URL        string `json:"url"`
	SigningKey string `json:"signingKey"`
}
type Channel struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Name     string           `json:"name"`
	Enabled  bool             `json:"enabled"`
	Events   []string         `json:"events"`
	Hint     string           `json:"hint"`
	Envelope keyring.Envelope `json:"-"`
}
type Repository interface {
	ListChannels(context.Context) ([]Channel, error)
	SaveChannel(context.Context, Channel, bool) error
	DeleteChannel(context.Context, string) error
}
type Message struct {
	Event   string    `json:"event"`
	Title   string    `json:"title"`
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
	Account string    `json:"account"`
	Machine string    `json:"machine"`
}

func AAD(id string) []byte { return []byte("instadeck:notification:" + id + ":v1") }

type Failure struct {
	Code       string
	RetryAfter time.Duration
}

func (f *Failure) Error() string { return f.Code }
