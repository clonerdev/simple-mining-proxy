package admin

import (
	"net/http"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/apierr"
	"github.com/famfamfam/simple-mining-proxy/internal/opshare"
)

var errNoOperatorShare = apierr.Conflict(apierr.M("operator_share_unavailable", "the operator share is not available on this server"))

// usageRange reads from and to (2006-01-02, UTC days) from the query; without
// them the last 30 days, today included.
func usageRange(r *http.Request, now time.Time) (from, to time.Time, err error) {
	to = now.UTC()
	from = to.AddDate(0, 0, -29)
	parse := func(name string, dst *time.Time) error {
		v := r.URL.Query().Get(name)
		if v == "" {
			return nil
		}
		t, perr := time.Parse("2006-01-02", v)
		if perr != nil {
			return apierr.Validation(apierr.M("bad_date", "{name} must be a date like 2006-01-02", "name", name), nil)
		}
		*dst = t
		return nil
	}
	if err = parse("from", &from); err != nil {
		return
	}
	if err = parse("to", &to); err != nil {
		return
	}
	if to.Before(from) {
		err = apierr.Validation(apierr.M("bad_range", "to is before from"), nil)
	}
	return
}

// operatorUsage is the account of the operator's share: for each device and
// day, how much of the work was credited to the operator and how much to the
// customer, from the shares the pool answered. It holds no money amounts: the
// pool pays whoever a share was credited to, and nothing here moves funds.
func (s *Server) operatorUsage(w http.ResponseWriter, r *http.Request) error {
	if s.d.OperatorShare == nil {
		return errNoOperatorShare
	}
	from, to, err := usageRange(r, time.Now())
	if err != nil {
		return err
	}
	rows := s.d.OperatorShare.Usage(from, to, r.URL.Query().Get("customer"))
	if rows == nil {
		rows = []opshare.DayUsage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.Format("2006-01-02"), "to": to.Format("2006-01-02"), "rows": rows})
	return nil
}

// operatorPools lists the pools the share switched itself off on, and why.
func (s *Server) operatorPools(w http.ResponseWriter, r *http.Request) error {
	if s.d.OperatorShare == nil {
		return errNoOperatorShare
	}
	off := s.d.OperatorShare.Pools()
	if off == nil {
		off = []opshare.PoolStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": s.d.OperatorShare.Active(), "pools_off": off})
	return nil
}
