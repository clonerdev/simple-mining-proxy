package accounts

// Messages are the English texts of the problems an *Error can carry, by key.
// The admin API sends the key (the web UI translates it) with this text for
// API clients and logs. Every key used in this package is listed here, and the
// web UI's locale test requires a translation of each one.
var Messages = map[string]string{
	"username_invalid":    "3–32 characters: lowercase Latin letters, digits, ., _ and -; starts with a letter or digit",
	"username_taken":      "this username is already taken",
	"password_short":      "at least {min} characters",
	"password_long":       "at most {max} characters",
	"password_username":   "the password must not be the username",
	"password_weak":       "the password is too simple",
	"role_unknown":        "unknown role",
	"display_name_long":   "at most {max} characters",
	"language_unknown":    "unknown language",
	"customer_required":   "a customer account needs a customer record",
	"customer_staff_only": "only customer accounts are linked to a customer",
	"permission_unknown":  "unknown permission: {permission}",
	"permissions_many":    "at most {max} permissions",
	"users_many":          "at most {max} accounts",
}
