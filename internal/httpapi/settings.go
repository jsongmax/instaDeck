package httpapi

import (
	"context"
	"net/http"
)

const (
	settingBlockInternalSSH = "block_internal_ssh"
	settingRequireReauth    = "require_reauth"
)

func (api *API) loadSwitches(ctx context.Context) error {
	block, err := api.dependencies.SetupStore.BoolSetting(ctx, settingBlockInternalSSH, true)
	if err != nil {
		return err
	}
	reauth, err := api.dependencies.SetupStore.BoolSetting(ctx, settingRequireReauth, true)
	if err != nil {
		return err
	}
	api.applySwitch(settingBlockInternalSSH, block)
	api.applySwitch(settingRequireReauth, reauth)
	return nil
}

func (api *API) applySwitch(key string, enabled bool) {
	switch key {
	case settingBlockInternalSSH:
		if api.dependencies.Terminal != nil {
			api.dependencies.Terminal.SetAddressFilter(enabled)
		}
	case settingRequireReauth:
		api.reauthOff.Store(!enabled)
	}
}

type settingsDTO struct {
	BlockInternalSSH bool `json:"blockInternalSsh"`
	RequireReauth    bool `json:"requireReauth"`
}

func (api *API) currentSettings(ctx context.Context) (settingsDTO, error) {
	block, err := api.dependencies.SetupStore.BoolSetting(ctx, settingBlockInternalSSH, true)
	if err != nil {
		return settingsDTO{}, err
	}
	reauth, err := api.dependencies.SetupStore.BoolSetting(ctx, settingRequireReauth, true)
	return settingsDTO{BlockInternalSSH: block, RequireReauth: reauth}, err
}

func (api *API) settings(w http.ResponseWriter, r *http.Request) {
	if api.dependencies.SetupStore == nil {
		fail(w, r, 503, "unavailable")
		return
	}
	value, err := api.currentSettings(r.Context())
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	writeJSON(w, 200, value)
}

func (api *API) changeSettings(w http.ResponseWriter, r *http.Request) {
	if api.dependencies.SetupStore == nil {
		fail(w, r, 503, "unavailable")
		return
	}
	var input struct {
		BlockInternalSSH *bool `json:"blockInternalSsh"`
		RequireReauth    *bool `json:"requireReauth"`
	}
	if !decode(w, r, &input, 1024) {
		return
	}
	changes := []struct {
		key   string
		value *bool
	}{{settingBlockInternalSSH, input.BlockInternalSSH}, {settingRequireReauth, input.RequireReauth}}
	weakens, present := false, false
	for _, change := range changes {
		if change.value != nil {
			present = true
			weakens = weakens || !*change.value
		}
	}
	if !present {
		fail(w, r, 400, "invalid_request")
		return
	}
	// Turning a protection off always needs fresh credentials, even when the
	// second confirmation switch is already off. A stolen session must not be
	// able to quietly lower the panel's defenses.
	if weakens && !api.recent(w, r) {
		return
	}
	if !api.requireAudit(w, r, "settings.change", "", "") {
		return
	}
	api.settingsMutex.Lock()
	defer api.settingsMutex.Unlock()
	for _, change := range changes {
		if change.value == nil {
			continue
		}
		if api.dependencies.SetupStore.SetBoolSetting(r.Context(), change.key, *change.value) != nil {
			fail(w, r, 503, "unavailable")
			return
		}
		api.applySwitch(change.key, *change.value)
		result := "disabled"
		if *change.value {
			result = "enabled"
		}
		_ = api.event(r, "settings."+change.key, result, "", "")
	}
	value, err := api.currentSettings(r.Context())
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	writeJSON(w, 200, value)
}
