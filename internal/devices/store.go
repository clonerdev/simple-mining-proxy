package devices

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/audit"
	"github.com/famfamfam/simple-mining-proxy/internal/store"
)

const (
	maxCustomers   = 5000
	maxDevices     = 50000
	maxPerCustomer = 2000
	maxName        = 64
	maxNotes       = 500
	maxReason      = 200
	maxLimit       = 10000
	flushEvery     = 30 * time.Second
)

// Languages a customer may prefer, for the Telegram bot and the reports.
var Languages = []string{"en", "ru", "fa"}

type file struct {
	Customers []Customer `json:"customers"`
	Devices   []Device   `json:"devices"`
}

// touch is what the Stratum side learns about a device on every login: it is
// kept in memory and written with the next change or the periodic flush, so
// an ASIC that reconnects every few seconds does not rewrite the file.
type touch struct {
	ua, ip string
	seen   time.Time
	denied int
}

// Store is the customers and devices file.
type Store struct {
	doc *store.Doc[file]
	now func() time.Time

	mu      sync.RWMutex
	custs   map[string]Customer // by id
	slugs   map[string]string   // slug -> id
	devs    map[string]Device   // by id
	keys    map[string]string   // customer id + "\x00" + worker key -> device id
	touches map[string]touch

	hmu      sync.Mutex
	handlers []func(Event)
}

// Open loads the file at path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	doc, err := store.OpenDoc[file](path, nil)
	if err != nil {
		return nil, err
	}
	s := &Store{doc: doc, now: time.Now, touches: map[string]touch{}}
	doc.Read(func(f *file) { s.reindex(f) })
	return s, nil
}

func (s *Store) reindex(f *file) {
	custs := make(map[string]Customer, len(f.Customers))
	slugs := make(map[string]string, len(f.Customers))
	for _, c := range f.Customers {
		custs[c.ID] = c
		slugs[c.Slug] = c.ID
	}
	devs := make(map[string]Device, len(f.Devices))
	keys := make(map[string]string, len(f.Devices))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range f.Devices {
		if t, ok := s.touches[d.ID]; ok {
			d = applyTouch(d, t)
		}
		devs[d.ID] = d
		keys[d.CustomerID+"\x00"+workerKey(d.Worker)] = d.ID
	}
	s.custs, s.slugs, s.devs, s.keys = custs, slugs, devs, keys
}

func applyTouch(d Device, t touch) Device {
	if t.seen.After(d.LastSeen) {
		d.LastSeen = t.seen
	}
	if t.ua != "" {
		d.UserAgent = t.ua
	}
	if t.ip != "" {
		d.LastIP = t.ip
	}
	d.Denied += t.denied
	return d
}

// ---- keys ----

const keyAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no 0 o 1 i l: it is typed into an ASIC

// KeyLength is the length of a connection key: about 119 bits.
const KeyLength = 24

// NewKey makes a connection key.
func NewKey() string {
	b := make([]byte, KeyLength)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = keyAlphabet[int(b[i])%len(keyAlphabet)]
	}
	return string(b)
}

func newSalt() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hashKey(salt, key string) string {
	sum := sha256.Sum256([]byte(salt + ":" + key))
	return hex.EncodeToString(sum[:])
}

// CheckKey reports whether key is the connection key of c. The key has far
// more entropy than a password, so a salted SHA-256 is enough, and it keeps
// a flood of wrong keys from costing the CPU a slow hash would.
func CheckKey(c Customer, key string) bool {
	got := hashKey(c.KeySalt, key)
	return subtle.ConstantTimeCompare([]byte(got), []byte(c.KeyHash)) == 1
}

var dummyCustomer = func() Customer {
	salt := newSalt()
	return Customer{KeySalt: salt, KeyHash: hashKey(salt, NewKey())}
}()

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ---- reads ----

// Customers returns every customer ordered by name.
func (s *Store) Customers() []Customer {
	s.mu.RLock()
	out := make([]Customer, 0, len(s.custs))
	for _, c := range s.custs {
		out = append(out, c)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// Customer returns a customer by id.
func (s *Store) Customer(id string) (Customer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.custs[id]
	return c, ok
}

// CustomerBySlug returns a customer by the short name in the ASIC login.
func (s *Store) CustomerBySlug(slug string) (Customer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.custs[s.slugs[strings.ToLower(slug)]]
	return c, ok
}

// Filter selects devices; zero fields match everything.
type Filter struct {
	CustomerID string
	Status     DeviceStatus
	Text       string // part of the worker name, case-insensitive
}

// Devices returns the devices matching f ordered by customer, then worker.
func (s *Store) Devices(f Filter) []Device {
	text := strings.ToLower(strings.TrimSpace(f.Text))
	s.mu.RLock()
	out := make([]Device, 0, len(s.devs))
	for _, d := range s.devs {
		if f.CustomerID != "" && d.CustomerID != f.CustomerID {
			continue
		}
		if f.Status != "" && d.Status != f.Status {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(d.Worker), text) {
			continue
		}
		out = append(out, d)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].CustomerID != out[j].CustomerID {
			return out[i].CustomerID < out[j].CustomerID
		}
		return strings.ToLower(out[i].Worker) < strings.ToLower(out[j].Worker)
	})
	return out
}

// Device returns a device by id.
func (s *Store) Device(id string) (Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devs[id]
	return d, ok
}

// DeviceFor returns the device of a customer with this worker name.
func (s *Store) DeviceFor(customerID, worker string) (Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devs[s.keys[customerID+"\x00"+workerKey(worker)]]
	return d, ok
}

// Counts is the number of customers and devices per state.
type Counts struct {
	Customers map[CustomerStatus]int `json:"customers"`
	Devices   map[DeviceStatus]int   `json:"devices"`
}

func (s *Store) Counts() Counts {
	c := Counts{Customers: map[CustomerStatus]int{}, Devices: map[DeviceStatus]int{}}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, x := range s.custs {
		c.Customers[x.Status]++
	}
	for _, d := range s.devs {
		c.Devices[d.Status]++
	}
	return c
}

// slotsLocked counts the devices of a customer that hold a slot, not counting
// except.
func (s *Store) slotsIn(f *file, customerID, except string) (slots, pending, total int) {
	for _, d := range f.Devices {
		if d.CustomerID != customerID {
			continue
		}
		total++
		if d.ID == except {
			continue
		}
		if d.Status.holdsSlot() {
			slots++
		}
		if d.Status == Pending {
			pending++
		}
	}
	return
}

// Usable reports whether the customer and the device may mine at time t, and
// if not, why (a short key). It is the one rule behind the login check, the
// check of every share and the sweep that cuts off expired devices.
func Usable(c Customer, d Device, t time.Time) (bool, string) {
	switch {
	case c.Status == CustomerBlocked:
		return false, "customer_blocked"
	case c.Status == CustomerSuspended:
		return false, "customer_suspended"
	case c.ExpiresAt != nil && !t.Before(*c.ExpiresAt):
		return false, "customer_expired"
	case d.Status != Approved:
		return false, "device_" + string(d.Status)
	case d.ExpiresAt != nil && !t.Before(*d.ExpiresAt):
		return false, "device_expired"
	}
	return true, ""
}

// Usable is the rule above for a device by id, with the current time.
func (s *Store) Usable(deviceID string) (bool, string) {
	s.mu.RLock()
	d, ok := s.devs[deviceID]
	c := s.custs[d.CustomerID]
	s.mu.RUnlock()
	if !ok {
		return false, "device_deleted"
	}
	return Usable(c, d, s.now())
}

// ---- customers ----

// NewCustomer is a request to create a customer.
type NewCustomer struct {
	Slug           string
	Name           string
	Account        string
	Language       string
	Notes          string
	MaxDevices     int
	MaxConnections int
	ExpiresAt      *time.Time
}

func checkCustomer(name, account, lang, notes string, maxDev, maxConn int) *Error {
	name = strings.TrimSpace(name)
	switch {
	case name == "" || len([]rune(name)) > maxName:
		return bad("name", "name_required", "max", maxName)
	case account != "" && !ValidAccount(account):
		return bad("account", "account_invalid")
	case lang != "" && !contains(Languages, lang):
		return bad("language", "language_unknown")
	case len([]rune(notes)) > maxNotes:
		return bad("notes", "notes_long", "max", maxNotes)
	case maxDev < 0 || maxDev > maxLimit:
		return bad("max_devices", "max_devices_range", "max", maxLimit)
	case maxConn < 0 || maxConn > maxLimit:
		return bad("max_connections", "max_connections_range", "max", maxLimit)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func actorName(a audit.Actor) string {
	if a.Name != "" {
		return a.Name
	}
	return a.Type
}

// CreateCustomer adds a customer and returns it with its connection key. The
// key is not kept: this is the only time it can be read.
func (s *Store) CreateCustomer(in NewCustomer, by audit.Actor) (Customer, string, error) {
	slug := strings.ToLower(strings.TrimSpace(in.Slug))
	if !ValidSlug(slug) {
		return Customer{}, "", bad("slug", "slug_invalid")
	}
	in.Name, in.Account = strings.TrimSpace(in.Name), strings.TrimSpace(in.Account)
	if e := checkCustomer(in.Name, in.Account, in.Language, in.Notes, in.MaxDevices, in.MaxConnections); e != nil {
		return Customer{}, "", e
	}
	key, salt := NewKey(), newSalt()
	now := s.now().UTC()
	c := Customer{
		ID: newID(), Slug: slug, Name: in.Name, Status: CustomerActive, StatusAt: now,
		Account: in.Account, MaxDevices: in.MaxDevices, MaxConnections: in.MaxConnections,
		ExpiresAt: in.ExpiresAt, Language: in.Language, Notes: strings.TrimSpace(in.Notes),
		KeySalt: salt, KeyHash: hashKey(salt, key), KeyRotatedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	err := s.doc.Mutate(func(f *file) error {
		if len(f.Customers) >= maxCustomers {
			return bad("", "customers_many", "max", maxCustomers)
		}
		for _, x := range f.Customers {
			if x.Slug == slug {
				return bad("slug", "slug_taken")
			}
		}
		f.Customers = append(f.Customers, c)
		return nil
	}, s.reindex)
	if err != nil {
		return Customer{}, "", err
	}
	s.emit(Event{Type: EvCustomerCreated, Actor: by, CustomerID: c.ID, CustomerSlug: c.Slug, CustomerName: c.Name})
	return c, key, nil
}

// CustomerUpdate changes a customer; nil fields stay as they are.
type CustomerUpdate struct {
	Name           *string
	Account        *string
	Language       *string
	Notes          *string
	MaxDevices     *int
	MaxConnections *int
	ExpiresAt      *time.Time
	ClearExpires   bool
}

func (s *Store) mutateCustomer(id string, fn func(c *Customer) *Error) (Customer, Customer, error) {
	var before, after Customer
	err := s.doc.Mutate(func(f *file) error {
		for i := range f.Customers {
			if f.Customers[i].ID != id {
				continue
			}
			before = f.Customers[i]
			if e := fn(&f.Customers[i]); e != nil {
				return e
			}
			f.Customers[i].UpdatedAt = s.now().UTC()
			after = f.Customers[i]
			return nil
		}
		return ErrCustomerNotFound
	}, s.reindex)
	return before, after, err
}

// UpdateCustomer changes the editable fields of a customer.
func (s *Store) UpdateCustomer(id string, in CustomerUpdate, by audit.Actor) (Customer, error) {
	before, after, err := s.mutateCustomer(id, func(c *Customer) *Error {
		if in.Name != nil {
			c.Name = strings.TrimSpace(*in.Name)
		}
		if in.Account != nil {
			c.Account = strings.TrimSpace(*in.Account)
		}
		if in.Language != nil {
			c.Language = *in.Language
		}
		if in.Notes != nil {
			c.Notes = strings.TrimSpace(*in.Notes)
		}
		if in.MaxDevices != nil {
			c.MaxDevices = *in.MaxDevices
		}
		if in.MaxConnections != nil {
			c.MaxConnections = *in.MaxConnections
		}
		switch {
		case in.ClearExpires:
			c.ExpiresAt = nil
		case in.ExpiresAt != nil:
			t := in.ExpiresAt.UTC()
			c.ExpiresAt = &t
		}
		return checkCustomer(c.Name, c.Account, c.Language, c.Notes, c.MaxDevices, c.MaxConnections)
	})
	if err != nil {
		return Customer{}, err
	}
	s.emit(Event{Type: EvCustomerUpdated, Actor: by, CustomerID: after.ID, CustomerSlug: after.Slug, CustomerName: after.Name,
		Details: customerDiff(before, after)})
	return after, nil
}

func customerDiff(a, b Customer) map[string]any {
	d := map[string]any{}
	if a.Name != b.Name {
		d["name"] = b.Name
	}
	if a.Account != b.Account {
		d["account"] = b.Account
	}
	if a.MaxDevices != b.MaxDevices {
		d["max_devices"] = b.MaxDevices
	}
	if a.MaxConnections != b.MaxConnections {
		d["max_connections"] = b.MaxConnections
	}
	if (a.ExpiresAt == nil) != (b.ExpiresAt == nil) || (a.ExpiresAt != nil && !a.ExpiresAt.Equal(*b.ExpiresAt)) {
		if b.ExpiresAt == nil {
			d["expires_at"] = "none"
		} else {
			d["expires_at"] = b.ExpiresAt.Format(time.RFC3339)
		}
	}
	if a.Language != b.Language {
		d["language"] = b.Language
	}
	return d
}

// SetCustomerStatus suspends, blocks or reactivates a customer. The devices
// of a customer that is not active cannot mine, whatever their own state.
func (s *Store) SetCustomerStatus(id string, status CustomerStatus, reason string, by audit.Actor) (Customer, error) {
	if !status.Valid() {
		return Customer{}, bad("status", "status_invalid")
	}
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > maxReason {
		return Customer{}, bad("reason", "reason_long", "max", maxReason)
	}
	before, after, err := s.mutateCustomer(id, func(c *Customer) *Error {
		c.Status, c.StatusReason, c.StatusAt = status, reason, s.now().UTC()
		return nil
	})
	if err != nil {
		return Customer{}, err
	}
	s.emit(Event{Type: EvCustomerStatus, Actor: by, CustomerID: after.ID, CustomerSlug: after.Slug, CustomerName: after.Name,
		From: string(before.Status), To: string(after.Status), Reason: reason})
	return after, nil
}

// RotateKey makes a new connection key; the old one stops working at once.
// The new key is returned once and not kept.
func (s *Store) RotateKey(id string, by audit.Actor) (string, error) {
	key, salt := NewKey(), newSalt()
	_, after, err := s.mutateCustomer(id, func(c *Customer) *Error {
		c.KeySalt, c.KeyHash, c.KeyRotatedAt = salt, hashKey(salt, key), s.now().UTC()
		return nil
	})
	if err != nil {
		return "", err
	}
	s.emit(Event{Type: EvCustomerKey, Actor: by, CustomerID: after.ID, CustomerSlug: after.Slug, CustomerName: after.Name})
	return key, nil
}

// DeleteCustomer removes a customer that has no devices left.
func (s *Store) DeleteCustomer(id string, by audit.Actor) error {
	var gone Customer
	err := s.doc.Mutate(func(f *file) error {
		idx := -1
		for i := range f.Customers {
			if f.Customers[i].ID == id {
				idx = i
			}
		}
		if idx < 0 {
			return ErrCustomerNotFound
		}
		for _, d := range f.Devices {
			if d.CustomerID == id {
				return bad("", "customer_has_devices")
			}
		}
		gone = f.Customers[idx]
		f.Customers = append(f.Customers[:idx], f.Customers[idx+1:]...)
		return nil
	}, s.reindex)
	if err != nil {
		return err
	}
	s.emit(Event{Type: EvCustomerDeleted, Actor: by, CustomerID: gone.ID, CustomerSlug: gone.Slug, CustomerName: gone.Name})
	return nil
}

// ---- devices ----

// ErrPendingLimit is returned by Register when the customer already has as
// many devices waiting for approval as allowed: the login is refused and no
// record is made, so that guessing worker names cannot fill the file.
var ErrPendingLimit = &Error{Key: "pending_many"}

func uniqueUpstream(f *file, customerID, worker string) string {
	base := UpstreamName(worker)
	taken := map[string]bool{}
	for _, d := range f.Devices {
		if d.CustomerID == customerID {
			taken[d.UpstreamWorker] = true
		}
	}
	name := base
	for n := 2; taken[name]; n++ {
		suffix := itoa(n)
		cut := base
		if len(cut)+len(suffix) > MaxUpstreamWorker {
			cut = cut[:MaxUpstreamWorker-len(suffix)]
		}
		name = cut + suffix
	}
	return name
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Register returns the device of a customer with this worker name, creating it
// as pending if the worker has not been seen before. created is true when a
// pending device was made. pendingLimit caps the pending devices per customer.
func (s *Store) Register(c Customer, worker, userAgent, ip string, pendingLimit int) (d Device, created bool, err error) {
	if !ValidWorker(worker) {
		return Device{}, false, bad("worker", "worker_invalid")
	}
	if cur, ok := s.DeviceFor(c.ID, worker); ok {
		return cur, false, nil
	}
	now := s.now().UTC()
	err = s.doc.Mutate(func(f *file) error {
		for _, x := range f.Devices {
			if x.CustomerID == c.ID && workerKey(x.Worker) == workerKey(worker) {
				d = x // registered by a concurrent login
				return nil
			}
		}
		_, pending, total := s.slotsIn(f, c.ID, "")
		if pendingLimit > 0 && pending >= pendingLimit {
			return ErrPendingLimit
		}
		if len(f.Devices) >= maxDevices || total >= maxPerCustomer {
			return bad("", "devices_many", "max", maxPerCustomer)
		}
		d = Device{
			ID: newID(), CustomerID: c.ID, Worker: worker, UpstreamWorker: uniqueUpstream(f, c.ID, worker),
			Status: Pending, StatusBy: "system", StatusAt: now, UserAgent: clip(userAgent, 128), LastIP: ip,
			FirstSeen: now, LastSeen: now,
		}
		f.Devices = append(f.Devices, d)
		created = true
		return nil
	}, s.reindex)
	if err != nil {
		return Device{}, false, err
	}
	if created {
		s.emit(Event{Type: EvDevicePending, At: now, Actor: audit.Actor{Type: audit.ActorDevice, Name: c.Slug + "." + worker, IP: ip},
			CustomerID: c.ID, CustomerSlug: c.Slug, CustomerName: c.Name, DeviceID: d.ID, Worker: worker, IP: ip,
			To: string(Pending), Details: map[string]any{"user_agent": d.UserAgent}})
	}
	return d, created, nil
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// NoteSeen records that a device logged in (or was refused). It only touches
// memory; Run writes it out.
func (s *Store) NoteSeen(deviceID, userAgent, ip string, denied bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devs[deviceID]
	if !ok {
		return
	}
	t := s.touches[deviceID]
	t.seen = s.now().UTC()
	if userAgent != "" {
		t.ua = clip(userAgent, 128)
	}
	if ip != "" {
		t.ip = ip
	}
	if denied {
		t.denied++
	}
	s.touches[deviceID] = t
	s.devs[deviceID] = applyTouch(d, touch{seen: t.seen, ua: clip(userAgent, 128), ip: ip, denied: boolInt(denied)})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Flush writes the pending touches to the file.
func (s *Store) Flush() error {
	s.mu.Lock()
	t := s.touches
	if len(t) == 0 {
		s.mu.Unlock()
		return nil
	}
	s.touches = map[string]touch{}
	s.mu.Unlock()
	err := s.doc.Mutate(func(f *file) error {
		for i := range f.Devices {
			if x, ok := t[f.Devices[i].ID]; ok {
				f.Devices[i] = applyTouch(f.Devices[i], x)
			}
		}
		return nil
	}, s.reindex)
	if err != nil {
		s.mu.Lock() // keep them for the next try
		for id, x := range t {
			y := s.touches[id]
			y.denied += x.denied
			if x.seen.After(y.seen) {
				y.seen, y.ua, y.ip = x.seen, x.ua, x.ip
			}
			s.touches[id] = y
		}
		s.mu.Unlock()
	}
	return err
}

// Run writes the touches every half minute and once more when ctx ends.
func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.Flush()
			return
		case <-t.C:
			_ = s.Flush()
		}
	}
}

var actionEvent = map[Action]string{
	ActApprove: EvDeviceApproved, ActReject: EvDeviceRejected, ActReopen: EvDeviceReopened,
	ActSuspend: EvDeviceSuspended, ActResume: EvDeviceResumed, ActBlock: EvDeviceBlocked,
	ActUnblock: EvDeviceUnblocked, ActRevoke: EvDeviceRevoked,
}

// Act changes the state of a device. The change is refused if the action does
// not fit the device's state, or if it would put the customer over its device
// limit. The live connections of a device that can no longer mine are closed
// by the handler of the resulting event.
func (s *Store) Act(deviceID string, action Action, reason string, by audit.Actor) (Device, error) {
	tr, ok := transitions[action]
	if !ok {
		return Device{}, bad("", "action_invalid")
	}
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > maxReason {
		return Device{}, bad("reason", "reason_long", "max", maxReason)
	}
	var before, after Device
	var cust Customer
	err := s.doc.Mutate(func(f *file) error {
		idx := -1
		for i := range f.Devices {
			if f.Devices[i].ID == deviceID {
				idx = i
			}
		}
		if idx < 0 {
			return ErrDeviceNotFound
		}
		d := &f.Devices[idx]
		before = *d
		if !statusIn(d.Status, tr.from) {
			return bad("", "transition_denied", "from", string(d.Status), "action", string(action))
		}
		to := tr.to
		if action == ActUnblock {
			to = d.SavedStatus
			if !to.Valid() || to == Blocked {
				to = Pending
			}
		}
		for _, c := range f.Customers {
			if c.ID == d.CustomerID {
				cust = c
			}
		}
		// Becoming a working device takes a slot; resuming one that already has
		// it does not.
		if to.holdsSlot() && !d.Status.holdsSlot() {
			slots, _, _ := s.slotsIn(f, d.CustomerID, d.ID)
			if cust.MaxDevices > 0 && slots >= cust.MaxDevices {
				return bad("", "device_limit", "max", cust.MaxDevices)
			}
		}
		now := s.now().UTC()
		if action == ActBlock {
			d.SavedStatus = d.Status
		} else if action == ActUnblock {
			d.SavedStatus = ""
		}
		d.History = append(d.History, Change{At: now, From: d.Status, To: to, By: actorName(by), Reason: reason})
		if len(d.History) > maxHistory {
			d.History = d.History[len(d.History)-maxHistory:]
		}
		d.Status, d.StatusReason, d.StatusBy, d.StatusAt = to, reason, actorName(by), now
		after = *d
		return nil
	}, s.reindex)
	if err != nil {
		return Device{}, err
	}
	s.emit(Event{Type: actionEvent[action], Actor: by, CustomerID: cust.ID, CustomerSlug: cust.Slug, CustomerName: cust.Name,
		DeviceID: after.ID, Worker: after.Worker, From: string(before.Status), To: string(after.Status), Reason: reason})
	return after, nil
}

func statusIn(s DeviceStatus, list []DeviceStatus) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// DeviceUpdate changes the limits and notes of a device; nil fields stay.
type DeviceUpdate struct {
	ExpiresAt      *time.Time
	ClearExpires   bool
	MaxConnections *int
	Notes          *string
}

// UpdateDevice changes the editable fields of a device.
func (s *Store) UpdateDevice(id string, in DeviceUpdate, by audit.Actor) (Device, error) {
	var before, after Device
	var cust Customer
	err := s.doc.Mutate(func(f *file) error {
		for i := range f.Devices {
			if f.Devices[i].ID != id {
				continue
			}
			d := &f.Devices[i]
			before = *d
			if in.MaxConnections != nil {
				if *in.MaxConnections < 0 || *in.MaxConnections > maxLimit {
					return bad("max_connections", "max_connections_range", "max", maxLimit)
				}
				d.MaxConnections = *in.MaxConnections
			}
			if in.Notes != nil {
				n := strings.TrimSpace(*in.Notes)
				if len([]rune(n)) > maxNotes {
					return bad("notes", "notes_long", "max", maxNotes)
				}
				d.Notes = n
			}
			switch {
			case in.ClearExpires:
				d.ExpiresAt = nil
			case in.ExpiresAt != nil:
				t := in.ExpiresAt.UTC()
				d.ExpiresAt = &t
			}
			after = *d
			for _, c := range f.Customers {
				if c.ID == d.CustomerID {
					cust = c
				}
			}
			return nil
		}
		return ErrDeviceNotFound
	}, s.reindex)
	if err != nil {
		return Device{}, err
	}
	details := map[string]any{}
	if before.MaxConnections != after.MaxConnections {
		details["max_connections"] = after.MaxConnections
	}
	if (before.ExpiresAt == nil) != (after.ExpiresAt == nil) || (before.ExpiresAt != nil && !before.ExpiresAt.Equal(*after.ExpiresAt)) {
		if after.ExpiresAt == nil {
			details["expires_at"] = "none"
		} else {
			details["expires_at"] = after.ExpiresAt.Format(time.RFC3339)
		}
	}
	s.emit(Event{Type: EvDeviceUpdated, Actor: by, CustomerID: cust.ID, CustomerSlug: cust.Slug, CustomerName: cust.Name,
		DeviceID: after.ID, Worker: after.Worker, Details: details})
	return after, nil
}

// DeleteDevice removes a device record. For a revoked or blocked device this
// is what lets the same worker name register again, so it is a deliberate,
// audited act; a device that is approved must be revoked or blocked first.
func (s *Store) DeleteDevice(id string, by audit.Actor) error {
	var gone Device
	var cust Customer
	err := s.doc.Mutate(func(f *file) error {
		idx := -1
		for i := range f.Devices {
			if f.Devices[i].ID == id {
				idx = i
			}
		}
		if idx < 0 {
			return ErrDeviceNotFound
		}
		d := f.Devices[idx]
		if d.Status == Approved || d.Status == Suspended {
			return bad("", "transition_denied", "from", string(d.Status), "action", "delete")
		}
		for _, c := range f.Customers {
			if c.ID == d.CustomerID {
				cust = c
			}
		}
		gone = d
		f.Devices = append(f.Devices[:idx], f.Devices[idx+1:]...)
		return nil
	}, s.reindex)
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.touches, id)
	s.mu.Unlock()
	s.emit(Event{Type: EvDeviceDeleted, Actor: by, CustomerID: cust.ID, CustomerSlug: cust.Slug, CustomerName: cust.Name,
		DeviceID: gone.ID, Worker: gone.Worker, From: string(gone.Status)})
	return nil
}
