package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/go-chi/chi/v5"
	"instadeck/internal/account"
	"instadeck/internal/instabox"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

func resourceKey(credential, request string) string {
	sum := sha256.Sum256([]byte(credential + "\x00" + request))
	return hex.EncodeToString(sum[:])
}
func resourceCode(err error, snapshot bool) string {
	var upstream *instabox.APIError
	if errors.As(err, &upstream) {
		switch {
		case snapshot && upstream.StatusCode == 403:
			return "snapshot_scope_missing"
		case upstream.Code == "machine_not_running":
			return "machine_not_running"
		case upstream.Code == "private_ports_unavailable":
			return "private_ports_unavailable"
		case upstream.StatusCode == 409:
			return "request_conflict"
		case upstream.StatusCode == 400:
			return "invalid_request"
		case upstream.StatusCode == 404:
			return "not_found"
		case !snapshot && upstream.StatusCode == 429 && upstream.Code != "rate_limited":
			return "port_limit_reached"
		}
	}
	return account.FilterError(err).Code
}

// The upstream minimum is 1024, and 5901 and 6768 are reserved.
func validPort(port int) bool { return port >= 1024 && port <= 65535 && port != 5901 && port != 6768 }
func (api *API) ports(w http.ResponseWriter, r *http.Request) {
	credential := r.URL.Query().Get("credentialId")
	var input struct {
		CredentialID string `json:"credentialId"`
		instabox.PublishPortRequest
	}
	event := "port.publish"
	if r.Method != "GET" {
		if !api.sensitive(w, r) {
			return
		}
		if r.Method == "POST" {
			if !decode(w, r, &input, 4096) {
				return
			}
			if input.CredentialID != "" {
				credential = input.CredentialID
			}
			if !validPort(input.Port) || len([]rune(input.Name)) > 100 || (input.Access != "public" && input.Access != "private") || (input.Protocol != "http" && input.Protocol != "tcp") || (input.Protocol == "tcp" && input.Access == "private") || (input.RotateToken && input.Access != "private") {
				fail(w, r, 400, "invalid_request")
				return
			}
		} else {
			event = "port.delete"
			port, err := strconv.Atoi(chi.URLParam(r, "port"))
			if err != nil || !validPort(port) {
				fail(w, r, 400, "invalid_request")
				return
			}
			input.Port = port
		}
	}
	if _, _, err := api.visibleMachine(r, credential); err != nil {
		fail(w, r, 404, "machine_not_visible")
		return
	}
	if r.Method != "GET" && !api.requireAudit(w, r, event, credential, chi.URLParam(r, "id")) {
		return
	}
	var result any
	err := api.dependencies.Accounts.WithResources(r.Context(), credential, func(a account.DTO, c account.ResourceClient) error {
		if r.Method != "GET" && a.Capabilities.Write == account.Denied {
			return &account.Failure{Code: "capability_denied"}
		}
		var err error
		switch r.Method {
		case "GET":
			var v instabox.PublicPortList
			v, err = c.ListPorts(r.Context(), chi.URLParam(r, "id"))
			for i := range v.Ports {
				if v.Ports[i].Protocol == "" {
					v.Ports[i].Protocol = "http"
				}
				v.Ports[i].Token = ""
				v.Ports[i].TokenURL = ""
			}
			if v.Ports == nil {
				v.Ports = []instabox.PublicPort{}
			}
			result = v
		case "POST":
			result, err = c.PublishPort(r.Context(), chi.URLParam(r, "id"), input.PublishPortRequest)
		case "DELETE":
			err = c.DeletePort(r.Context(), chi.URLParam(r, "id"), input.Port)
			result = map[string]bool{"ok": err == nil}
		}
		return err
	})
	if r.Method != "GET" {
		outcome := "success"
		if err != nil {
			outcome = "failure"
		}
		detail := ""
		if err != nil {
			detail = resourceCode(err, false)
		}
		_ = api.eventDetail(r, event, outcome, credential, chi.URLParam(r, "id"), detail)
	}
	if err != nil {
		fail(w, r, 400, resourceCode(err, false))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

type snapshotDTO struct {
	instabox.Snapshot
	CredentialID string `json:"credentialId"`
	AccountName  string `json:"accountName"`
}

func cleanSnapshot(v instabox.Snapshot) instabox.Snapshot {
	r := []rune(v.Error)
	if len(r) > 200 {
		v.Error = string(r[:200])
	}
	return v
}
func (api *API) snapshots(w http.ResponseWriter, r *http.Request) {
	accounts, err := api.dependencies.Accounts.List(r.Context())
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	values := []snapshotDTO{}
	failures := []map[string]string{}
	var mutex sync.Mutex
	var group sync.WaitGroup
	for _, a := range accounts {
		if !a.Enabled {
			continue
		}
		group.Add(1)
		go func(a account.DTO) {
			defer group.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			var list instabox.SnapshotList
			err := api.dependencies.Accounts.WithResources(ctx, a.CredentialID, func(a account.DTO, c account.ResourceClient) error {
				var e error
				list, e = c.ListSnapshots(ctx)
				return e
			})
			mutex.Lock()
			defer mutex.Unlock()
			if err != nil {
				failures = append(failures, map[string]string{"credentialId": a.CredentialID, "code": resourceCode(err, true)})
				return
			}
			for _, v := range list.Snapshots {
				if v.OrgID == a.OrgID {
					values = append(values, snapshotDTO{cleanSnapshot(v), a.CredentialID, a.Name})
				}
			}
		}(a)
	}
	group.Wait()
	sort.Slice(values, func(i, j int) bool {
		if values[i].CredentialID != values[j].CredentialID {
			return values[i].CredentialID < values[j].CredentialID
		}
		if values[i].CreatedAt != values[j].CreatedAt {
			return values[i].CreatedAt > values[j].CreatedAt
		}
		return values[i].ID < values[j].ID
	})
	sort.Slice(failures, func(i, j int) bool { return failures[i]["credentialId"] < failures[j]["credentialId"] })
	writeJSON(w, 200, map[string]any{"snapshots": values, "errors": failures})
}

var snapshotName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (api *API) changeSnapshot(w http.ResponseWriter, r *http.Request) {
	if !api.sensitive(w, r) {
		return
	}
	var input struct {
		CredentialID    string `json:"credentialId"`
		ClientRequestID string `json:"clientRequestId"`
		ConfirmName     string `json:"confirmName"`
		instabox.CreateSnapshotRequest
	}
	if !decode(w, r, &input, 16384) {
		return
	}
	if input.CredentialID == "" {
		input.CredentialID = r.URL.Query().Get("credentialId")
	}
	if input.ClientRequestID == "" || len(input.ClientRequestID) > 128 || len(input.Description) > 8192 {
		fail(w, r, 400, "invalid_request")
		return
	}
	event := "snapshot.create"
	machine := ""
	if r.Method == "POST" {
		machine = chi.URLParam(r, "id")
		if !snapshotName.MatchString(input.Name) {
			fail(w, r, 400, "invalid_request")
			return
		}
		if _, _, err := api.visibleMachine(r, input.CredentialID); err != nil {
			fail(w, r, 404, "machine_not_visible")
			return
		}
	} else {
		event = "snapshot.delete"
	}
	if !api.requireAudit(w, r, event, input.CredentialID, machine) {
		return
	}
	var result any = map[string]bool{"ok": true}
	err := api.dependencies.Accounts.WithResources(r.Context(), input.CredentialID, func(a account.DTO, c account.ResourceClient) error {
		key := resourceKey(input.CredentialID, input.ClientRequestID)
		if r.Method == "POST" {
			v, err := c.CreateSnapshot(r.Context(), machine, input.CreateSnapshotRequest, key)
			result = cleanSnapshot(v)
			return err
		}
		list, err := c.ListSnapshots(r.Context())
		if err != nil {
			return err
		}
		for _, v := range list.Snapshots {
			if v.ID == chi.URLParam(r, "id") && v.OrgID == a.OrgID {
				if input.ConfirmName != v.Name {
					return &account.Failure{Code: "confirmation_mismatch"}
				}
				return c.DeleteSnapshot(r.Context(), v.ID, key)
			}
		}
		return &account.Failure{Code: "not_found"}
	})
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	detail := ""
	if err != nil {
		detail = resourceCode(err, true)
	}
	_ = api.eventDetail(r, event, outcome, input.CredentialID, machine, detail)
	if err != nil {
		fail(w, r, 400, resourceCode(err, true))
		return
	}
	writeJSON(w, 200, result)
}
