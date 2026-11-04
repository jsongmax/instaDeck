package identity

import (
	"strings"
	"testing"
	"time"
)

func newReviewService(t *testing.T) *Service {
	t.Helper()
	service, err := New(&memoryRepo{}, testCipher{}, nil, Options{
		ChallengeTTL:      time.Minute,
		IdleTTL:           time.Hour,
		AbsoluteTTL:       24 * time.Hour,
		RecentTTL:         10 * time.Minute,
		ChallengeAttempts: 5,
	}, NewLimiter(time.Minute, 5, 100))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestBlockedAddressDoesNotConsumeGlobalBudget(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := NewLimiter(time.Minute, 2, 3)

	for i := 0; i < 2; i++ {
		if !limiter.Allow("192.0.2.1", now) {
			t.Fatalf("attempt %d from first address should be allowed", i)
		}
	}
	for i := 0; i < 50; i++ {
		if limiter.Allow("192.0.2.1", now) {
			t.Fatal("first address should be blocked")
		}
	}
	if !limiter.Allow("192.0.2.2", now) {
		t.Fatal("blocked address exhausted the global budget")
	}
	if limiter.Allow("192.0.2.3", now) {
		t.Fatal("global limit should now apply")
	}
}

func TestInitializeRejectsShortPassword(t *testing.T) {
	service := newReviewService(t)
	secret := []byte(strings.Repeat("s", 20))

	for _, password := range []string{"", "elevenchars"} {
		if _, err := service.Initialize(t.Context(), "admin-id", "admin", password, secret); err == nil {
			t.Errorf("password of %d characters was accepted", len(password))
		}
	}
	if _, err := service.Initialize(t.Context(), "admin-id", "admin", "twelve chars", secret); err != nil {
		t.Errorf("12 character password rejected: %v", err)
	}
}
