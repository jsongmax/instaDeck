package notify

import (
	"context"
	"instadeck/internal/account"
	"instadeck/internal/fleet"
	"slices"
	"sync"
	"time"
)

type fleetReader interface {
	GetFleet(context.Context) (fleet.Response, error)
}

type machineState struct {
	state string
	seen  time.Time
}
type Monitor struct {
	service    *Service
	fleet      fleetReader
	Accounts   *account.Service
	mutex      sync.Mutex
	states     map[string]machineState
	last       map[string]time.Time
	operations map[string]time.Time
	inFlight   map[string]bool
	interval   time.Duration
}

func NewMonitor(service *Service, fleets fleetReader) *Monitor {
	return &Monitor{service: service, fleet: fleets, states: map[string]machineState{}, last: map[string]time.Time{}, operations: map[string]time.Time{}, inFlight: map[string]bool{}}
}
func (m *Monitor) Begin(machine string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.inFlight[machine] = true
}
func (m *Monitor) Operation(machine string, now time.Time) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.operations[machine] = now
	delete(m.inFlight, machine)
}
func (m *Monitor) Compare(machines []fleet.Machine, accounts map[string]string, now time.Time) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	for _, v := range machines {
		if v.Stale || v.Conflict {
			continue
		}
		old, exists := m.states[v.ID]
		m.states[v.ID] = machineState{string(v.State), now}
		if !exists {
			continue
		}
		event := ""
		if v.State == "error" && old.state != "error" {
			event = "machine.error"
		}
		if old.state == "running" && v.State == "stopped" && !m.inFlight[v.ID] && (m.operations[v.ID].IsZero() || now.Sub(m.operations[v.ID]) > 2*time.Minute) {
			event = "machine.stopped"
		}
		if event == "" {
			continue
		}
		key := v.ID + ":" + event
		if previous, ok := m.last[key]; ok && now.Sub(previous) < 10*time.Minute {
			continue
		}
		m.last[key] = now
		account := ""
		if len(v.Sources) > 0 {
			account = accounts[v.Sources[0].CredentialID]
		}
		title, text := "机器进入错误状态", "这台机器进入了 error 状态，请检查。"
		if event == "machine.stopped" {
			title, text = "机器异常停止", "这台机器不是由面板操作停止的。"
		}
		m.service.Emit(Message{Event: event, Title: title, Message: text, Machine: v.Name, Account: account, Time: now})
	}
}

// Run leaves the polling ticker stopped until a channel subscribes to machine events.
func (m *Monitor) Run(ctx context.Context) {
	var ticker *time.Ticker
	var ticks <-chan time.Time
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	reload := func() {
		channels, err := m.service.repo.ListChannels(ctx)
		enabled := false
		if err == nil {
			for _, c := range channels {
				if c.Enabled && (slices.Contains(c.Events, "machine.error") || slices.Contains(c.Events, "machine.stopped")) {
					enabled = true
				}
			}
		}
		if !enabled && ticker != nil {
			ticker.Stop()
			ticker = nil
			ticks = nil
			m.mutex.Lock()
			m.states = map[string]machineState{}
			m.mutex.Unlock()
		}
		if enabled && ticker == nil {
			interval := m.interval
			if interval == 0 {
				interval = 60 * time.Second
			}
			ticker = time.NewTicker(interval)
			ticks = ticker.C
		}
	}
	reload()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.service.changed:
			reload()
		case <-ticks:
			value, _ := m.fleet.GetFleet(ctx)
			names := map[string]string{}
			if m.Accounts != nil {
				values, _ := m.Accounts.List(ctx)
				for _, a := range values {
					names[a.CredentialID] = a.Name
				}
			}
			m.Compare(value.Machines, names, time.Now().UTC())
		}
	}
}
