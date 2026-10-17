package devices

import "errors"

// Messages are the English texts of the problems an *Error can carry, by key.
// The admin API sends the key (the web UI translates it, as "device_<key>")
// with this text for API clients. Every key used in this package is listed
// here, and the web UI's locale test requires a translation of each one.
var Messages = map[string]string{
	"slug_invalid":          "3–32 characters: lowercase Latin letters, digits, _ and -; starts with a letter or digit",
	"slug_taken":            "this short name is already used by another customer",
	"name_required":         "the name is required, at most {max} characters",
	"account_invalid":       "letters, digits, _ and - only, at most 64 characters",
	"max_devices_range":     "from 0 (no limit) to {max}",
	"max_connections_range": "from 0 (no limit) to {max}",
	"language_unknown":      "unknown language",
	"notes_long":            "at most {max} characters",
	"status_invalid":        "unknown status",
	"customers_many":        "at most {max} customers",
	"customer_has_devices":  "the customer still has devices: delete them first",
	"worker_invalid":        "letters, digits, ., _ and - only, at most 64 characters",
	"action_invalid":        "unknown action",
	"transition_denied":     "the device is {from}: {action} is not possible now",
	"pending_many":          "too many devices are waiting for approval",
	"device_limit":          "the customer already has {max} devices; raise the limit first",
	"devices_many":          "at most {max} devices",
	"reason_long":           "at most {max} characters",
}

// Error is a problem with a request that the caller can show to the operator.
// Key is a stable message key, Field the input it is about.
type Error struct {
	Key    string
	Field  string
	Params map[string]any
}

func (e *Error) Error() string { return "devices: " + e.Key }

var (
	// ErrCustomerNotFound is returned for an unknown customer id.
	ErrCustomerNotFound = errors.New("devices: customer not found")
	// ErrDeviceNotFound is returned for an unknown device id.
	ErrDeviceNotFound = errors.New("devices: device not found")
)

func bad(field, key string, kv ...any) *Error {
	e := &Error{Key: key, Field: field}
	if len(kv) > 0 {
		e.Params = map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			e.Params[kv[i].(string)] = kv[i+1]
		}
	}
	return e
}
