package instabox

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestCreateOptionalValues(t *testing.T) {
	raw, err := json.Marshal(CreateMachineRequest{})
	if err != nil || string(raw) != "{}" {
		t.Fatalf("unexpected default body: %s", raw)
	}
	idle := AutoSuspendAfterSeconds(0)
	maximum := MaxRunMinutes(0)
	raw, err = json.Marshal(CreateMachineRequest{AutoSuspendAfterSeconds: &idle, MaxRunMinutes: &maximum, EnvSecrets: []SecretName{}, Secrets: &SecretSelection{Names: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"autoSuspendAfterSeconds", "maxRunMinutes"} {
		if string(fields[field]) != "0" {
			t.Fatalf("explicit zero lost: %s", field)
		}
	}
	for _, field := range []string{"envSecrets", "secrets"} {
		if string(fields[field]) != "[]" {
			t.Fatalf("explicit empty selection lost: %s", field)
		}
	}
	for _, encoded := range []string{`"all"`, `[]`, `["example"]`} {
		var selection SecretSelection
		if err = json.Unmarshal([]byte(encoded), &selection); err != nil {
			t.Fatal(err)
		}
		result, err := json.Marshal(selection)
		if err != nil || string(result) != encoded {
			t.Fatal("secret selection round trip failed")
		}
	}
}

func TestMachineStateFormatting(t *testing.T) {
	for _, state := range []MachineState{MachineStateRunning, MachineStateSuspended, "future_state"} {
		if actual := fmt.Sprintf("%s", state); actual != string(state) {
			t.Fatalf("state formatting: %q", actual)
		}
	}
}
