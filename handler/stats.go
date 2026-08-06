package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/briqt/singbox-panel/model"
)

var (
	errBadDay   = errors.New("dates must be formatted as YYYY-MM-DD")
	errBadRange = errors.New("from must not be later than to")
	errBadGroup = errors.New("group accepts any combination of day, user and node")
)

// Statistics are aggregated server-side only: every endpoint here returns
// numbers the UI renders as-is, so a chart and a table can never disagree.
type StatsHandler struct {
	Users   *model.UserStore
	Nodes   *model.NodeStore
	Traffic *model.TrafficStore
}

const dayLayout = "2006-01-02"

// defaultRangeDays is the window the UI opens on when no range is given.
const defaultRangeDays = 7

// HandleMeta tells the UI which calendar the numbers live in and how far back
// it may ask, so the date picker cannot offer a range that was already pruned.
func (h *StatsHandler) HandleMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone":         h.Traffic.LocationName(),
		"today":            h.Traffic.Today(),
		"retention_from":   h.Traffic.RetentionStart(),
		"retention_months": model.RetentionMonths,
	})
}

// HandleUsage answers every "who used how much, when, on which node" question
// through one grouping parameter: group=day,user,node in any combination
// (empty = one total row).
func (h *StatsHandler) HandleUsage(w http.ResponseWriter, r *http.Request) {
	q, err := h.parseUsageQuery(r, defaultRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := h.Traffic.Usage(q)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// HandleMyUsage is the same aggregation locked to the caller's own account.
func (h *StatsHandler) HandleMyUsage(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(int)
	if userID == 0 {
		writeError(w, http.StatusNotFound, "session has no user record")
		return
	}
	q, err := h.parseUsageQuery(r, 30)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q.UserID = userID
	q.ByUser = false
	rows, err := h.Traffic.Usage(q)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *StatsHandler) HandleUserStats(w http.ResponseWriter, _ *http.Request) {
	users, err := h.Users.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type UserStat struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		Enabled    bool   `json:"enabled"`
		UsedBytes  int64  `json:"used_bytes"`
		UpBytes    int64  `json:"up_bytes"`
		DownBytes  int64  `json:"down_bytes"`
		LimitBytes int64  `json:"limit_bytes"`
		ExpireAt   string `json:"expire_at"`
	}
	stats := make([]UserStat, 0, len(users))
	for _, u := range users {
		stats = append(stats, UserStat{
			ID: u.ID, Name: u.Name, Enabled: u.Enabled,
			UsedBytes: u.TrafficUsedBytes, UpBytes: u.TrafficUpBytes, DownBytes: u.TrafficDownBytes,
			LimitBytes: u.TrafficLimitBytes, ExpireAt: u.ExpireAt,
		})
	}
	writeJSON(w, http.StatusOK, stats)
}

// HandleNodeStats reports each node's traffic over the retained window, not
// since the beginning of time — pruned samples are gone by design.
func (h *StatsHandler) HandleNodeStats(w http.ResponseWriter, _ *http.Request) {
	nodes, err := h.Nodes.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	totals, err := h.Traffic.NodeTotals()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type NodeStat struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		UpBytes   int64  `json:"up_bytes"`
		DownBytes int64  `json:"down_bytes"`
	}
	stats := make([]NodeStat, 0, len(nodes))
	for _, n := range nodes {
		t := totals[n.ID]
		stats = append(stats, NodeStat{
			ID: n.ID, Name: n.Name, Enabled: n.Enabled,
			UpBytes: t[0], DownBytes: t[1],
		})
	}
	writeJSON(w, http.StatusOK, stats)
}

// parseUsageQuery clamps the requested range into the retained window so a
// stale bookmark degrades to the oldest readable day instead of an empty chart.
func (h *StatsHandler) parseUsageQuery(r *http.Request, defaultDays int) (model.UsageQuery, error) {
	query := r.URL.Query()
	today := h.Traffic.Today()
	retentionFrom := h.Traffic.RetentionStart()

	to, err := parseDay(query.Get("to"), today)
	if err != nil {
		return model.UsageQuery{}, err
	}
	if to > today {
		to = today
	}
	if to < retentionFrom {
		return model.UsageQuery{}, fmt.Errorf("usage older than %s is no longer kept", retentionFrom)
	}
	defaultFrom, err := shiftDay(to, -(defaultDays - 1))
	if err != nil {
		return model.UsageQuery{}, err
	}
	from, err := parseDay(query.Get("from"), defaultFrom)
	if err != nil {
		return model.UsageQuery{}, err
	}
	if from < retentionFrom {
		from = retentionFrom
	}
	if from > to {
		return model.UsageQuery{}, errBadRange
	}

	q := model.UsageQuery{From: from, To: to}
	for _, dim := range strings.Split(query.Get("group"), ",") {
		switch strings.TrimSpace(dim) {
		case "":
		case "day":
			q.ByDay = true
		case "user":
			q.ByUser = true
		case "node":
			q.ByNode = true
		default:
			return model.UsageQuery{}, errBadGroup
		}
	}
	q.UserID, _ = strconv.Atoi(query.Get("user_id"))
	q.NodeID, _ = strconv.Atoi(query.Get("node_id"))
	return q, nil
}

func parseDay(value, fallback string) (string, error) {
	if value == "" {
		return fallback, nil
	}
	if _, err := time.Parse(dayLayout, value); err != nil {
		return "", errBadDay
	}
	return value, nil
}

func shiftDay(day string, delta int) (string, error) {
	t, err := time.Parse(dayLayout, day)
	if err != nil {
		return "", errBadDay
	}
	return t.AddDate(0, 0, delta).Format(dayLayout), nil
}
