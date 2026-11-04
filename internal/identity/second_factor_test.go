package identity

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSecondFactorFailureBudgetAcrossChallengesAndMethods(t *testing.T) {
	service, _, now, codes := fixture(t)
	service.options.IdleTTL = time.Hour
	token := login(t, service, *now)
	for index := 0; index < 10; index++ {
		var err error
		switch index % 3 {
		case 0:
			_, err = service.Complete(context.Background(), challenge(t, service), "invalid", false)
		case 1:
			_, err = service.Complete(context.Background(), challenge(t, service), "invalid", true)
		case 2:
			_, err = service.Reauthenticate(context.Background(), token, "test-password", "invalid", "192.0.2.1")
		}
		if err == nil {
			t.Fatal("invalid factor accepted")
		}
	}
	*now = now.Add(time.Minute)
	for _, recovery := range []bool{false, true} {
		code := totp([]byte("12345678901234567890"), now.Unix()/30, 6)
		if recovery {
			code = codes[0]
		}
		_, err := service.Complete(context.Background(), challenge(t, service), code, recovery)
		if err == nil || !strings.Contains(err.Error(), "second factor rate limited") {
			t.Errorf("valid factor still checked after ten failures: recovery=%v err=%v", recovery, err)
		}
	}
	_, err := service.Reauthenticate(context.Background(), token, "test-password", totp([]byte("12345678901234567890"), now.Unix()/30, 6), "192.0.2.99")
	if err == nil || !strings.Contains(err.Error(), "second factor rate limited") {
		t.Errorf("reauthentication bypassed shared budget: %v", err)
	}
	*now = now.Add(14 * time.Minute)
	if _, err = service.Complete(context.Background(), challenge(t, service), codes[0], true); err != nil {
		t.Fatal("window did not recover", err)
	}
}

func TestSecondFactorSuccessAndInvalidChallengesDoNotConsumeBudget(t *testing.T) {
	service, _, now, codes := fixture(t)
	for index := 0; index < 20; index++ {
		_, _ = service.Complete(context.Background(), "unknown-challenge", "invalid", false)
		login(t, service, *now)
		*now = now.Add(30 * time.Second)
	}
	for index := 0; index < 9; index++ {
		_, _ = service.Complete(context.Background(), challenge(t, service), "invalid", true)
	}
	if _, err := service.Complete(context.Background(), challenge(t, service), codes[0], true); err != nil {
		t.Fatal("success or public invalid challenge consumed budget", err)
	}
}

func TestSecondFactorConcurrentFailuresCannotExceedBudget(t *testing.T) {
	service, _, _, codes := fixture(t)
	challenges := make([]string, 32)
	for index := range challenges {
		challenges[index] = challenge(t, service)
	}
	var workers sync.WaitGroup
	var limited atomic.Int32
	start := make(chan struct{})
	for _, value := range challenges {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := service.Complete(context.Background(), value, "invalid", true)
			if err != nil && strings.Contains(err.Error(), "second factor rate limited") {
				limited.Add(1)
			}
		}()
	}
	close(start)
	workers.Wait()
	if limited.Load() != 22 {
		t.Errorf("rate limited %d requests, want 22", limited.Load())
	}
	if _, err := service.Complete(context.Background(), challenge(t, service), codes[0], true); err == nil {
		t.Fatal("failure budget bypassed")
	}
}
