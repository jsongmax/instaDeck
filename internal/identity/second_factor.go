package identity

import (
	"sync"
	"time"
)

type secondFactorBudget struct {
	mutex    sync.Mutex
	failures [10]time.Time
	count    int
}

func (budget *secondFactorBudget) allow(now time.Time) bool {
	remaining := 0
	for index := 0; index < budget.count; index++ {
		if now.Before(budget.failures[index].Add(15 * time.Minute)) {
			budget.failures[remaining] = budget.failures[index]
			remaining++
		}
	}
	budget.count = remaining
	return remaining < len(budget.failures)
}

func (budget *secondFactorBudget) fail(now time.Time) {
	if budget.count < len(budget.failures) {
		budget.failures[budget.count] = now
		budget.count++
	}
}
