package httpapi

import (
	"errors"
	"github.com/go-chi/chi/v5"
	"instadeck/internal/notify"
	"net/http"
	"strings"
)

func (api *API) notificationChannels(w http.ResponseWriter, r *http.Request) {
	service := api.dependencies.Notify
	if service == nil {
		fail(w, r, 503, "unavailable")
		return
	}
	if r.Method == "GET" {
		values, err := service.List(r.Context())
		if err != nil {
			fail(w, r, 503, "unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"channels": values})
		return
	}
	if !api.sensitive(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	event := "notification.change"
	if strings.HasSuffix(r.URL.Path, "/test") {
		event = "notification.test"
	}
	var input struct {
		Type    *string       `json:"type"`
		Name    *string       `json:"name"`
		Enabled *bool         `json:"enabled"`
		Events  *[]string     `json:"events"`
		Config  notify.Config `json:"config"`
	}
	if event != "notification.test" && r.Method != "DELETE" && !decode(w, r, &input, 16384) {
		return
	}
	if !api.requireAudit(w, r, event, "", "") {
		return
	}
	var err error
	if event == "notification.test" {
		err = service.Test(r.Context(), id)
	} else if r.Method == "DELETE" {
		err = service.Delete(r.Context(), id)
	} else {
		c := notify.Channel{ID: id, Enabled: true, Events: []string{}}
		if r.Method == "PATCH" {
			values, e := service.List(r.Context())
			err = e
			found := false
			for _, v := range values {
				if v.ID == id {
					c = v
					found = true
				}
			}
			if !found {
				err = &notify.Failure{Code: "not_found"}
			}
		}
		if input.Type != nil {
			c.Type = *input.Type
		}
		if input.Name != nil {
			c.Name = *input.Name
		}
		if input.Enabled != nil {
			c.Enabled = *input.Enabled
		}
		if input.Events != nil {
			c.Events = *input.Events
		}
		if err == nil {
			_, err = service.Save(r.Context(), c, input.Config, r.Method == "POST")
		}
	}
	outcome, code := "success", ""
	if err != nil {
		outcome, code = "failure", "unavailable"
		var failure *notify.Failure
		if errors.As(err, &failure) {
			code = failure.Code
		}
	}
	_ = api.eventDetail(r, event, outcome, "", "", code)
	if err != nil {
		fail(w, r, 400, code)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
