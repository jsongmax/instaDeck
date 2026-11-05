package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"instadeck/internal/account"
	"instadeck/internal/fleet"
	"instadeck/internal/instabox"
	"instadeck/internal/operation"
	"net/http"
	"strings"
	"time"
	"unicode"
)

func (api *API) registerBusiness(router chi.Router) {
	api.registerSecurity(router)
	router.Get("/notification-channels", api.notificationChannels)
	router.Post("/notification-channels", api.notificationChannels)
	router.Patch("/notification-channels/{id}", api.notificationChannels)
	router.Delete("/notification-channels/{id}", api.notificationChannels)
	router.Post("/notification-channels/{id}/test", api.notificationChannels)
	router.Get("/machines/{id}/files", api.files)
	router.Get("/machines/{id}/files/content", api.files)
	router.Put("/machines/{id}/files/content", api.files)
	router.Put("/machines/{id}/files/parts", api.files)
	router.Post("/machines/{id}/files/uploads/complete", api.files)
	router.Post("/machines/{id}/files/uploads/abort", api.files)
	router.Post("/machines/{id}/files/folders", api.files)
	router.Post("/machines/{id}/files/rename", api.files)
	router.Post("/machines/{id}/files/delete", api.files)
	router.Get("/machines/{id}/ports", api.ports)
	router.Post("/machines/{id}/ports", api.ports)
	router.Delete("/machines/{id}/ports/{port}", api.ports)
	router.Get("/snapshots", api.snapshots)
	router.Post("/machines/{id}/snapshots", api.changeSnapshot)
	router.Delete("/snapshots/{id}", api.changeSnapshot)
	router.Get("/session", api.session)
	router.Post("/auth/logout", api.logout)
	router.Post("/auth/reauthenticate", api.reauthenticate)
	router.Get("/accounts", api.accounts)
	router.Post("/accounts", api.changeAccount)
	router.Patch("/accounts/{id}", api.changeAccount)
	router.Delete("/accounts/{id}", api.changeAccount)
	router.Post("/accounts/{id}/validate", api.validateAccount)
	router.Get("/fleet", api.getFleet)
	router.Get("/machines/{id}", api.machine)
	router.Patch("/machines/{id}", api.renameMachine)
	router.Get("/templates", api.templates)
	router.Get("/audit-events", api.auditEvents)
	router.Get("/audit-events/export", api.exportAuditEvents)
	router.Get("/settings", api.settings)
	router.Patch("/settings", api.changeSettings)
	router.Get("/actions/{id}", api.action)
	router.Post("/machines", api.submit)
	router.Post("/machines/{id}/actions", api.submit)
	router.Post("/machines/{id}/terminal-tickets", api.ticket)
	router.Get("/terminal/ws", api.websocket)
}

func (api *API) accounts(w http.ResponseWriter, r *http.Request) {
	values, err := api.dependencies.Accounts.List(r.Context())
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"accounts": values})
}

type accountRequest struct {
	Name         string                `json:"name"`
	Key          string                `json:"key"`
	Declarations *account.Declarations `json:"declarations"`
	Enabled      *bool                 `json:"enabled"`
}

func (api *API) changeAccount(w http.ResponseWriter, r *http.Request) {
	if !api.sensitive(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	var input accountRequest
	if r.Method != "DELETE" && !decode(w, r, &input, 16384) {
		return
	}
	if len(input.Key) > 8192 || len(input.Name) > 128 || (r.Method == "POST" && (input.Key == "" || input.Name == "")) {
		fail(w, r, 400, "invalid_request")
		return
	}
	if !api.requireAudit(w, r, "credential.change", id, "") {
		return
	}
	var declarations account.Declarations
	if input.Declarations != nil {
		declarations = *input.Declarations
	}
	value := account.Input{Name: input.Name, Key: input.Key, Declarations: declarations}
	var result account.DTO
	var err error
	switch r.Method {
	case "POST":
		result, err = api.dependencies.Accounts.Add(r.Context(), value)
	case "DELETE":
		err = api.dependencies.Accounts.Delete(r.Context(), id)
	case "PATCH":
		if input.Key != "" {
			result, err = api.dependencies.Accounts.Replace(r.Context(), id, value)
		} else if input.Name != "" {
			result, err = api.dependencies.Accounts.Rename(r.Context(), id, input.Name)
		}
		if err == nil && input.Declarations != nil && input.Key == "" {
			result, err = api.dependencies.Accounts.SetDeclarations(r.Context(), id, declarations)
		}
		if err == nil && input.Enabled != nil {
			result, err = api.dependencies.Accounts.SetEnabled(r.Context(), id, *input.Enabled)
		}
		if input.Key == "" && input.Name == "" && input.Declarations == nil && input.Enabled == nil {
			fail(w, r, 400, "invalid_request")
			return
		}
	}
	if id != "" && err == nil {
		api.dependencies.Fleet.Invalidate(id)
		if input.Key != "" || r.Method == "DELETE" || (input.Enabled != nil && !*input.Enabled) {
			api.dependencies.Terminal.CloseByCredential(id)
		}
	}
	if err != nil {
		_ = api.eventDetail(r, "credential.change", "failure", id, "", account.FilterError(err).Code)
		api.accountFailure(w, r, err)
		return
	}
	if id == "" {
		_ = api.event(r, "credential.change", "success", result.CredentialID, "")
	} else {
		_ = api.event(r, "credential.change", "success", id, "")
	}
	if r.Method == "DELETE" {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	writeJSON(w, 200, result)
}

func (api *API) validateAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !api.requireAudit(w, r, "credential.validate", id, "") {
		return
	}
	value, err := api.dependencies.Accounts.Validate(r.Context(), id)
	api.dependencies.Fleet.Invalidate(id)
	if err != nil {
		_ = api.eventDetail(r, "credential.validate", "failure", id, "", account.FilterError(err).Code)
		api.accountFailure(w, r, err)
		return
	}
	_ = api.event(r, "credential.validate", "success", id, "")
	writeJSON(w, 200, value)
}

func (api *API) accountFailure(w http.ResponseWriter, r *http.Request, err error) {
	failure := account.FilterError(err)
	requestID, _ := r.Context().Value(requestIDKey).(string)
	api.dependencies.Logger.Warn("account request failed", "code", failure.Code, "detail", failure.Diagnostic(), "requestId", requestID)
	fail(w, r, 400, failure.Code)
}

type usageDTO struct {
	Machines       int64      `json:"machines"`
	Snapshots      int64      `json:"snapshots"`
	StoredBytes    int64      `json:"storedBytes"`
	LogicalBytes   int64      `json:"logicalBytes"`
	ExclusiveBytes int64      `json:"exclusiveBytes"`
	ComputedAt     *time.Time `json:"computedAt"`
	Limits         limitsDTO  `json:"limits"`
}

type limitsDTO struct {
	MaxMachines  int64    `json:"maxMachines"`
	MaxSnapshots int64    `json:"maxSnapshots"`
	MaxStoredGiB int64    `json:"maxStoredGiB"`
	Unlimited    []string `json:"unlimited"`
}

type fleetAccountDTO struct {
	CredentialID   string           `json:"credentialId"`
	Complete       bool             `json:"complete"`
	Stale          bool             `json:"stale"`
	FetchedAt      *time.Time       `json:"fetchedAt"`
	Error          *account.Failure `json:"error"`
	Usage          *usageDTO        `json:"usage"`
	UsageFetchedAt *time.Time       `json:"usageFetchedAt"`
	UsageError     *account.Failure `json:"usageError"`
}

func (api *API) getFleet(w http.ResponseWriter, r *http.Request) {
	value, err := api.dependencies.Fleet.GetFleet(r.Context())
	var failure *account.Failure
	if err != nil && !(errors.As(err, &failure) && failure.Code == "all_accounts_failed") && len(value.Accounts) == 0 {
		fail(w, r, 503, "unavailable")
		return
	}
	accounts := make([]fleetAccountDTO, 0, len(value.Accounts))
	for _, current := range value.Accounts {
		result := fleetAccountDTO{CredentialID: current.CredentialID, Complete: current.Complete, Stale: current.Stale, FetchedAt: current.FetchedAt, Error: current.Error, UsageFetchedAt: current.UsageFetchedAt, UsageError: current.UsageError}
		if usage := current.Usage; usage != nil {
			result.Usage = &usageDTO{Machines: usage.Machines, Snapshots: usage.Snapshots, StoredBytes: usage.StoredBytes, LogicalBytes: usage.LogicalBytes, ExclusiveBytes: usage.ExclusiveBytes, ComputedAt: usage.ComputedAt, Limits: limitsDTO{MaxMachines: usage.Limits.MaxMachines, MaxSnapshots: usage.Limits.MaxSnapshots, MaxStoredGiB: usage.Limits.MaxStoredGiB, Unlimited: usage.Limits.Unlimited}}
		}
		accounts = append(accounts, result)
	}
	writeJSON(w, 200, struct {
		Machines []fleet.Machine   `json:"machines"`
		Accounts []fleetAccountDTO `json:"accounts"`
		Partial  bool              `json:"partial"`
	}{Machines: value.Machines, Accounts: accounts, Partial: value.Partial})
}

type machineDTO struct {
	ID        string                `json:"id"`
	OrgID     string                `json:"orgId"`
	Name      string                `json:"name"`
	State     instabox.MachineState `json:"state"`
	Ready     bool                  `json:"ready"`
	Spec      instabox.MachineSpec  `json:"spec"`
	CreatedAt time.Time             `json:"createdAt"`
	UpdatedAt time.Time             `json:"updatedAt"`
}

func (api *API) visibleMachine(r *http.Request, credentialID string) (account.DTO, instabox.Machine, error) {
	metadata, client, err := api.dependencies.Accounts.ConnectionClientForCredential(r.Context(), credentialID, 0)
	if err != nil {
		return account.DTO{}, instabox.Machine{}, err
	}
	machineID := chi.URLParam(r, "id")
	visible, err := api.dependencies.Fleet.Visible(r.Context(), credentialID, metadata.OrgID, machineID)
	if err != nil || !visible {
		return account.DTO{}, instabox.Machine{}, errors.New("machine unavailable")
	}
	value, err := client.GetMachine(r.Context(), machineID)
	if err != nil || value.ID != machineID || (value.OrgID != "" && value.OrgID != metadata.OrgID) {
		return account.DTO{}, instabox.Machine{}, errors.New("machine unavailable")
	}
	value.OrgID = metadata.OrgID
	return metadata, value, nil
}

func (api *API) machine(w http.ResponseWriter, r *http.Request) {
	_, value, err := api.visibleMachine(r, r.URL.Query().Get("credentialId"))
	if err != nil {
		fail(w, r, 404, "not_found")
		return
	}
	writeJSON(w, 200, machineDTO{ID: value.ID, OrgID: value.OrgID, Name: value.Name, State: value.State, Ready: value.Runtime.Ready, Spec: value.Spec, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt})
}

func (api *API) templates(w http.ResponseWriter, r *http.Request) {
	_, client, err := api.dependencies.Accounts.ConnectionClientForCredential(r.Context(), r.URL.Query().Get("credentialId"), 0)
	if err != nil {
		fail(w, r, 400, "credential_rejected")
		return
	}
	value, err := client.ListTemplates(r.Context())
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	var me instabox.Me
	if identityClient, identityErr := api.dependencies.Accounts.ClientForCredential(r.Context(), r.URL.Query().Get("credentialId")); identityErr == nil {
		if identity, identityErr := identityClient.GetMe(r.Context()); identityErr == nil {
			me = identity
		}
	}
	type templateDTO struct {
		PlanAllowed bool                 `json:"planAllowed"`
		ID          string               `json:"id"`
		Version     string               `json:"version"`
		Arch        string               `json:"arch"`
		Creatable   bool                 `json:"creatable"`
		Default     bool                 `json:"default"`
		DefaultSpec instabox.MachineSpec `json:"defaultSpec"`
	}
	values := make([]templateDTO, 0, len(value.Templates))
	for _, template := range value.Templates {
		values = append(values, templateDTO{PlanAllowed: templatePlanAllowed(me.Org.Tier, template.ID), ID: template.ID, Version: template.Version, Arch: template.Arch, Creatable: template.Creatable, Default: template.Default, DefaultSpec: template.DefaultSpec})
	}
	writeJSON(w, 200, map[string]any{"templates": values})
}

type actionDTO struct {
	ID            string    `json:"id"`
	CredentialID  string    `json:"credentialId"`
	MachineID     string    `json:"machineId"`
	Kind          string    `json:"kind"`
	State         string    `json:"state"`
	UpstreamState string    `json:"upstreamState"`
	Phase         string    `json:"phase"`
	ErrorCode     string    `json:"errorCode"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func publicAction(value operation.Action) actionDTO {
	phase := []rune(strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value.Phase))
	if len(phase) > 128 {
		phase = phase[:128]
	}
	state := value.UpstreamState
	switch state {
	case "", "queued", "running", "succeeded", "failed":
	default:
		state = "unknown"
	}
	return actionDTO{ID: value.ID, CredentialID: value.CredentialID, MachineID: value.MachineID, Kind: value.Kind, State: value.State, UpstreamState: state, Phase: string(phase), ErrorCode: value.ErrorCode, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func (api *API) action(w http.ResponseWriter, r *http.Request) {
	value, err := api.dependencies.Operations.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, 404, "not_found")
		return
	}
	writeJSON(w, 200, publicAction(value))
}

func (api *API) submit(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ClientRequestID string          `json:"clientRequestId"`
		CredentialID    string          `json:"credentialId"`
		Kind            string          `json:"kind"`
		ConfirmName     string          `json:"confirmName"`
		Body            json.RawMessage `json:"body"`
	}
	if !decode(w, r, &input, 32768) {
		return
	}
	if strings.TrimSpace(input.ClientRequestID) == "" || len(input.ClientRequestID) > 128 || input.CredentialID == "" || len(input.CredentialID) > 128 || len(input.ConfirmName) > 128 {
		fail(w, r, 400, "invalid_request")
		return
	}
	machineID := chi.URLParam(r, "id")
	if machineID == "" {
		input.Kind = "create"
	} else if input.Kind == "create" {
		fail(w, r, 400, "invalid_request")
		return
	}
	if input.Kind == "delete" && !api.sensitive(w, r) {
		return
	}
	if input.Kind == "create" {
		var body instabox.CreateMachineRequest
		if len(input.Body) == 0 || json.Unmarshal(input.Body, &body) != nil || body.OrgID != "" {
			fail(w, r, 400, "invalid_request")
			return
		}
		metadata, _, err := api.dependencies.Accounts.WriteClientForCredential(r.Context(), input.CredentialID, 0)
		if err != nil {
			fail(w, r, 400, "credential_unavailable")
			return
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(input.Body, &fields)
		if fields == nil {
			fail(w, r, 400, "invalid_request")
			return
		}
		fields["orgId"], _ = json.Marshal(metadata.OrgID)
		input.Body, _ = json.Marshal(fields)
	}
	requestID, _ := r.Context().Value(requestIDKey).(string)
	value, err := api.dependencies.Operations.Submit(r.Context(), operation.Intent{AdministratorID: currentSession(r).AdministratorID, RequestID: requestID, SourceAddress: api.clientIP(r), ClientRequestID: input.ClientRequestID, CredentialID: input.CredentialID, Kind: input.Kind, MachineID: machineID, ConfirmName: input.ConfirmName, Body: input.Body})
	if err != nil {
		code := "operation_rejected"
		status := 400
		var failure *operation.Failure
		if errors.As(err, &failure) {
			code = failure.Code
			if code == "storage_unavailable" || code == "audit_unavailable" {
				status = 503
			}
		}
		fail(w, r, status, code)
		return
	}
	writeJSON(w, 202, publicAction(value))
}

// Free-plan template restrictions: https://api.instabox.sh/v1/openapi.json (POST /v1/machines).
func templatePlanAllowed(tier, id string) bool {
	if tier != "free" {
		return true
	}
	switch id {
	case "agent-browser", "android", "ci":
		return true
	}
	return false
}
