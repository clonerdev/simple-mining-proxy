package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/apierr"
	"github.com/famfamfam/simple-mining-proxy/internal/devices"
)

var errNoDevices = apierr.Conflict(apierr.M("devices_unavailable", "device control is not available on this server"))

// devicesErr turns an error of the devices package into an API error. The
// key of a problem is "device_" plus the package's key; the web UI translates
// it, and the English text goes along for API clients.
func devicesErr(err error) error {
	var e *devices.Error
	switch {
	case errors.As(err, &e):
		text := devices.Messages[e.Key]
		for k, v := range e.Params {
			text = strings.ReplaceAll(text, "{"+k+"}", fmt.Sprint(v))
		}
		msg := apierr.Msg{Key: "device_" + e.Key, Params: e.Params, Text: text}
		switch e.Key {
		case "transition_denied", "device_limit", "pending_many", "customers_many", "devices_many", "customer_has_devices":
			return apierr.Conflict(msg)
		}
		// A problem about one input goes on that field; one about the request
		// as a whole (an unknown action) is the message itself.
		if e.Field == "" {
			return apierr.Validation(msg, nil)
		}
		return apierr.Validation(apierr.M("device_invalid", "the request is invalid"), map[string]apierr.Msg{e.Field: msg})
	case errors.Is(err, devices.ErrCustomerNotFound):
		return apierr.NotFound(apierr.M("customer_not_found", "customer not found"))
	case errors.Is(err, devices.ErrDeviceNotFound):
		return apierr.NotFound(apierr.M("device_not_found", "device not found"))
	}
	return err
}

// live is what the Stratum side knows of a device right now.
type live struct {
	Sessions   int     `json:"sessions"`
	HashrateHs float64 `json:"hashrate_hs"`
	Accepted   uint64  `json:"accepted"`
	Rejected   uint64  `json:"rejected"`
}

// liveByDevice sums the connected sessions per device.
func (s *Server) liveByDevice(now time.Time) map[string]*live {
	out := map[string]*live{}
	if s.d.Registry == nil {
		return out
	}
	for _, sess := range s.d.Registry.List() {
		ids := sess.DeviceIDs()
		if len(ids) == 0 {
			continue
		}
		in := sess.Info(now)
		// A session that carries several devices (one connection, several
		// authorized workers) counts for each, with its share split evenly, so
		// the customer's total stays the sum of the parts.
		n := float64(len(ids))
		for _, id := range ids {
			l := out[id]
			if l == nil {
				l = &live{}
				out[id] = l
			}
			l.Sessions++
			l.HashrateHs += in.HashrateHs / n
			l.Accepted += in.Accepted / uint64(len(ids))
			l.Rejected += in.Rejected / uint64(len(ids))
		}
	}
	return out
}

// customerView is a customer as the API shows it: never the key hash or salt.
type customerView struct {
	ID             string                       `json:"id"`
	Slug           string                       `json:"slug"`
	Name           string                       `json:"name"`
	Status         devices.CustomerStatus       `json:"status"`
	StatusReason   string                       `json:"status_reason,omitempty"`
	StatusAt       *time.Time                   `json:"status_at,omitempty"`
	Account        string                       `json:"account,omitempty"`
	MaxDevices     int                          `json:"max_devices"`
	MaxConnections int                          `json:"max_connections"`
	ExpiresAt      *time.Time                   `json:"expires_at,omitempty"`
	Language       string                       `json:"language,omitempty"`
	Notes          string                       `json:"notes,omitempty"`
	KeyRotatedAt   time.Time                    `json:"key_rotated_at"`
	CreatedAt      time.Time                    `json:"created_at"`
	UpdatedAt      time.Time                    `json:"updated_at"`
	Devices        map[devices.DeviceStatus]int `json:"devices"`
	Live           live                         `json:"live"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// customerViewOf builds the view; staff is false for the customer's own view,
// which leaves out the operator's private notes.
func (s *Server) customerViewOf(c devices.Customer, devs []devices.Device, lv map[string]*live, staff bool) customerView {
	v := customerView{
		ID: c.ID, Slug: c.Slug, Name: c.Name, Status: c.Status, StatusReason: c.StatusReason, StatusAt: timePtr(c.StatusAt),
		Account: c.Account, MaxDevices: c.MaxDevices, MaxConnections: c.MaxConnections, ExpiresAt: c.ExpiresAt,
		Language: c.Language, KeyRotatedAt: c.KeyRotatedAt, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		Devices: map[devices.DeviceStatus]int{},
	}
	if staff {
		v.Notes = c.Notes
	}
	for _, d := range devs {
		if d.CustomerID != c.ID {
			continue
		}
		v.Devices[d.Status]++
		if l := lv[d.ID]; l != nil {
			v.Live.Sessions += l.Sessions
			v.Live.HashrateHs += l.HashrateHs
			v.Live.Accepted += l.Accepted
			v.Live.Rejected += l.Rejected
		}
	}
	return v
}

// deviceView is a device with what is live of it.
type deviceView struct {
	devices.Device
	CustomerSlug string `json:"customer_slug"`
	CustomerName string `json:"customer_name"`
	Live         live   `json:"live"`
	// Usable and Why say whether the device may mine at this moment.
	Usable bool   `json:"usable"`
	Why    string `json:"why,omitempty"`
}

func (s *Server) deviceViewOf(d devices.Device, lv map[string]*live, staff bool) deviceView {
	v := deviceView{Device: d}
	if c, ok := s.d.Devices.Customer(d.CustomerID); ok {
		v.CustomerSlug, v.CustomerName = c.Slug, c.Name
	}
	v.Usable, v.Why = s.d.Devices.Usable(d.ID)
	if l := lv[d.ID]; l != nil {
		v.Live = *l
	}
	if !staff {
		v.Notes, v.StatusBy, v.LastIP = "", "", ""
		v.Device.History = nil
	}
	return v
}

// ---- staff: customers ----

func (s *Server) listCustomers(w http.ResponseWriter, r *http.Request) error {
	if s.d.Devices == nil {
		return errNoDevices
	}
	now := time.Now()
	lv, devs := s.liveByDevice(now), s.d.Devices.Devices(devices.Filter{})
	out := []customerView{}
	for _, c := range s.d.Devices.Customers() {
		out = append(out, s.customerViewOf(c, devs, lv, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"customers": out, "counts": s.d.Devices.Counts(),
		"control": s.d.Settings.Get().DeviceControlEnabled(), "languages": devices.Languages,
	})
	return nil
}

type customerInput struct {
	Slug           string  `json:"slug"`
	Name           string  `json:"name"`
	Account        string  `json:"account"`
	Language       string  `json:"language"`
	Notes          string  `json:"notes"`
	MaxDevices     int     `json:"max_devices"`
	MaxConnections int     `json:"max_connections"`
	ExpiresAt      *string `json:"expires_at"`
}

// parseExpiry reads an optional RFC 3339 time; "" and nil mean none.
func parseExpiry(v *string) (*time.Time, error) {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(*v))
	if err != nil {
		return nil, apierr.Validation(apierr.M("device_invalid", "the request is invalid"),
			map[string]apierr.Msg{"expires_at": apierr.M("device_expires_invalid", "use a date and time like 2027-01-31T00:00:00Z")})
	}
	t = t.UTC()
	return &t, nil
}

func (s *Server) createCustomer(w http.ResponseWriter, r *http.Request) error {
	if s.d.Devices == nil {
		return errNoDevices
	}
	var in customerInput
	if err := decode(r, &in); err != nil {
		return err
	}
	exp, err := parseExpiry(in.ExpiresAt)
	if err != nil {
		return err
	}
	who := principalOf(r)
	c, key, err := s.d.Devices.CreateCustomer(devices.NewCustomer{
		Slug: in.Slug, Name: in.Name, Account: in.Account, Language: in.Language, Notes: in.Notes,
		MaxDevices: in.MaxDevices, MaxConnections: in.MaxConnections, ExpiresAt: exp,
	}, who.actor(clientIP(r)))
	if err != nil {
		return devicesErr(err)
	}
	// The key is shown here, once: only its hash is kept.
	writeJSON(w, http.StatusCreated, map[string]any{
		"customer": s.customerViewOf(c, nil, nil, true), "key": key,
	})
	return nil
}

func (s *Server) getCustomer(w http.ResponseWriter, r *http.Request) error {
	if s.d.Devices == nil {
		return errNoDevices
	}
	c, ok := s.d.Devices.Customer(r.PathValue("id"))
	if !ok {
		return devicesErr(devices.ErrCustomerNotFound)
	}
	lv, devs := s.liveByDevice(time.Now()), s.d.Devices.Devices(devices.Filter{CustomerID: c.ID})
	views := make([]deviceView, 0, len(devs))
	for _, d := range devs {
		views = append(views, s.deviceViewOf(d, lv, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{"customer": s.customerViewOf(c, devs, lv, true), "devices": views})
	return nil
}

func (s *Server) updateCustomer(w http.ResponseWriter, r *http.Request) error {
	if s.d.Devices == nil {
		return errNoDevices
	}
	var in struct {
		Name           *string `json:"name"`
		Account        *string `json:"account"`
		Language       *string `json:"language"`
		Notes          *string `json:"notes"`
		MaxDevices     *int    `json:"max_devices"`
		MaxConnections *int    `json:"max_connections"`
		ExpiresAt      *string `json:"expires_at"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	up := devices.CustomerUpdate{Name: in.Name, Account: in.Account, Language: in.Language, Notes: in.Notes,
		MaxDevices: in.MaxDevices, MaxConnections: in.MaxConnections}
	if in.ExpiresAt != nil {
		exp, err := parseExpiry(in.ExpiresAt)
		if err != nil {
			return err
		}
		up.ExpiresAt, up.ClearExpires = exp, exp == nil
	}
	c, err := s.d.Devices.UpdateCustomer(r.PathValue("id"), up, principalOf(r).actor(clientIP(r)))
	if err != nil {
		return devicesErr(err)
	}
	lv, devs := s.liveByDevice(time.Now()), s.d.Devices.Devices(devices.Filter{CustomerID: c.ID})
	writeJSON(w, http.StatusOK, s.customerViewOf(c, devs, lv, true))
	return nil
}

func (s *Server) setCustomerStatus(w http.ResponseWriter, r *http.Request) error {
	if s.d.Devices == nil {
		return errNoDevices
	}
	var in struct {
		Status devices.CustomerStatus `json:"status"`
		Reason string                 `json:"reason"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	c, err := s.d.Devices.SetCustomerStatus(r.PathValue("id"), in.Status, in.Reason, principalOf(r).actor(clientIP(r)))
	if err != nil {
		return devicesErr(err)
	}
	lv, devs := s.liveByDevice(time.Now()), s.d.Devices.Devices(devices.Filter{CustomerID: c.ID})
	writeJSON(w, http.StatusOK, s.customerViewOf(c, devs, lv, true))
	return nil
}

func (s *Server) rotateCustomerKey(w http.ResponseWriter, r *http.Request) error {
	if s.d.Devices == nil {
		return errNoDevices
	}
	key, err := s.d.Devices.RotateKey(r.PathValue("id"), principalOf(r).actor(clientIP(r)))
	if err != nil {
		return devicesErr(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key})
	return nil
}

func (s *Server) deleteCustomer(w http.ResponseWriter, r *http.Request) error {
	if s.d.Devices == nil {
		return errNoDevices
	}
	if err := s.d.Devices.DeleteCustomer(r.PathValue("id"), principalOf(r).actor(clientIP(r))); err != nil {
		return devicesErr(err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

// ---- staff: devices ----

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) error {
	if s.d.Devices == nil {
		return errNoDevices
	}
	q := r.URL.Query()
	status := devices.DeviceStatus(q.Get("status"))
	if status != "" && !status.Valid() {
		return apierr.Validation(apierr.M("device_invalid", "the request is invalid"),
			map[string]apierr.Msg{"status": apierr.M("device_status_invalid", "unknown status")})
	}
	lv := s.liveByDevice(time.Now())
	out := []deviceView{}
	for _, d := range s.d.Devices.Devices(devices.Filter{CustomerID: q.Get("customer"), Status: status, Text: q.Get("q")}) {
		out = append(out, s.deviceViewOf(d, lv, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out, "counts": s.d.Devices.Counts(), "actions": devices.Actions})
	return nil
}

func (s *Server) actOnDevice(w http.ResponseWriter, r *http.Request) error {
	if s.d.Devices == nil {
		return errNoDevices
	}
	var in struct {
		Action devices.Action `json:"action"`
		Reason string         `json:"reason"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	d, err := s.d.Devices.Act(r.PathValue("id"), in.Action, in.Reason, principalOf(r).actor(clientIP(r)))
	if err != nil {
		return devicesErr(err)
	}
	writeJSON(w, http.StatusOK, s.deviceViewOf(d, s.liveByDevice(time.Now()), true))
	return nil
}

func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) error {
	if s.d.Devices == nil {
		return errNoDevices
	}
	var in struct {
		ExpiresAt      *string `json:"expires_at"`
		MaxConnections *int    `json:"max_connections"`
		Notes          *string `json:"notes"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	up := devices.DeviceUpdate{MaxConnections: in.MaxConnections, Notes: in.Notes}
	if in.ExpiresAt != nil {
		exp, err := parseExpiry(in.ExpiresAt)
		if err != nil {
			return err
		}
		up.ExpiresAt, up.ClearExpires = exp, exp == nil
	}
	d, err := s.d.Devices.UpdateDevice(r.PathValue("id"), up, principalOf(r).actor(clientIP(r)))
	if err != nil {
		return devicesErr(err)
	}
	writeJSON(w, http.StatusOK, s.deviceViewOf(d, s.liveByDevice(time.Now()), true))
	return nil
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) error {
	if s.d.Devices == nil {
		return errNoDevices
	}
	if err := s.d.Devices.DeleteDevice(r.PathValue("id"), principalOf(r).actor(clientIP(r))); err != nil {
		return devicesErr(err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

// ---- a customer's own view ----

// selfCustomer is the customer behind the signed-in account. The id comes
// from the account on the server, never from the request, so a customer
// cannot ask for another customer's data by changing a number.
func (s *Server) selfCustomer(r *http.Request) (devices.Customer, error) {
	if s.d.Devices == nil {
		return devices.Customer{}, errNoDevices
	}
	id := principalOf(r).CustomerID
	if id == "" {
		return devices.Customer{}, apierr.NotFound(apierr.M("self_no_customer", "this account is not linked to a customer"))
	}
	c, ok := s.d.Devices.Customer(id)
	if !ok {
		return devices.Customer{}, apierr.NotFound(apierr.M("self_no_customer", "this account is not linked to a customer"))
	}
	return c, nil
}

func (s *Server) selfView(w http.ResponseWriter, r *http.Request) error {
	c, err := s.selfCustomer(r)
	if err != nil {
		return err
	}
	lv, devs := s.liveByDevice(time.Now()), s.d.Devices.Devices(devices.Filter{CustomerID: c.ID})
	views := make([]deviceView, 0, len(devs))
	for _, d := range devs {
		views = append(views, s.deviceViewOf(d, lv, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"customer": s.customerViewOf(c, devs, lv, false), "devices": views})
	return nil
}
