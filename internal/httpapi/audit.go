package httpapi

import (
	"context"
	"encoding/csv"
	"instadeck/internal/store/sqlite"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type filteredAuditStore interface {
	FilterAuditEvents(context.Context, int64, int, sqlite.AuditFilter) ([]sqlite.AuditRecord, error)
}

func (api *API) auditPage(w http.ResponseWriter, r *http.Request, limit int) ([]sqlite.AuditRecord, bool) {
	q := r.URL.Query()
	after := int64(0)
	if raw := q.Get("after"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			fail(w, r, 400, "invalid_request")
			return nil, false
		}
		after = n
	}
	f := sqlite.AuditFilter{CredentialID: q.Get("credentialId"), MachineID: q.Get("machineId"), Type: q.Get("type"), Result: q.Get("result")}
	switch q.Get("order") {
	case "":
	case "desc":
		f.Descending = true
		if raw := q.Get("before"); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n <= 0 {
				fail(w, r, 400, "invalid_request")
				return nil, false
			}
			f.Before = n
		}
	default:
		fail(w, r, 400, "invalid_request")
		return nil, false
	}
	if ids := q.Get("machineIds"); ids != "" {
		f.MachineIDs = strings.Split(ids, ",")
		if len(f.MachineIDs) > 256 {
			fail(w, r, 400, "invalid_request")
			return nil, false
		}
	}
	for _, item := range []struct {
		name   string
		target **time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		if raw := q.Get(item.name); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				fail(w, r, 400, "invalid_request")
				return nil, false
			}
			*item.target = &t
		}
	}
	if f.Since != nil && f.Until != nil && f.Since.After(*f.Until) {
		fail(w, r, 400, "invalid_request")
		return nil, false
	}
	store, ok := api.dependencies.Audit.(filteredAuditStore)
	if !ok {
		fail(w, r, 503, "unavailable")
		return nil, false
	}
	values, err := store.FilterAuditEvents(r.Context(), after, limit, f)
	if err != nil {
		fail(w, r, 503, "unavailable")
		return nil, false
	}
	return values, true
}
func (api *API) auditEvents(w http.ResponseWriter, r *http.Request) {
	if values, ok := api.auditPage(w, r, 100); ok {
		writeJSON(w, 200, map[string]any{"events": values})
	}
}
func csvSafe(value string) string {
	if value != "" && strings.ContainsRune("=+-@\t\r", rune(value[0])) {
		return "'" + value
	}
	return value
}
func (api *API) exportAuditEvents(w http.ResponseWriter, r *http.Request) {
	values, ok := api.auditPage(w, r, 10000)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="instadeck-audit.csv"`)
	writer := csv.NewWriter(w)
	defer writer.Flush()
	_ = writer.Write([]string{"time", "type", "credentialId", "machineId", "actionId", "operationKind", "result", "detail"})
	for _, v := range values {
		e := v.Event
		row := []string{e.Time.Format(time.RFC3339), e.Type, e.CredentialID, e.MachineID, e.ActionID, v.OperationKind, e.Result, e.Detail()}
		for i := range row {
			row[i] = csvSafe(row[i])
		}
		if writer.Write(row) != nil {
			return
		}
	}
}
