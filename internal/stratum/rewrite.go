package stratum

import "strings"

// Rewrite is the result of mapping an ASIC login to the pool account.
type Rewrite struct {
	User     string
	Password string
}

// Worker extracts {worker}: the part after the first dot, the whole login
// when there is no dot, "default" when nothing follows the dot.
func Worker(login string) string {
	i := strings.IndexByte(login, '.')
	if i < 0 {
		if login == "" {
			return "default"
		}
		return login
	}
	if w := login[i+1:]; w != "" {
		return w
	}
	return "default"
}

// Vars are the values a pool template can use besides the ASIC's own login:
// the customer's pool account and the worker name made for the pool. They come
// from device control and are empty without it.
type Vars struct {
	Account string // {account}
	Worker  string // {upstream_worker}
}

// RewriteLogin maps an ASIC login and password to one pool account.
func RewriteLogin(login, asicPassword, userTemplate, poolPassword string) Rewrite {
	return RewriteWith(login, asicPassword, userTemplate, poolPassword, Vars{})
}

// RewriteWith is RewriteLogin with the values of device control as well.
func RewriteWith(login, asicPassword, userTemplate, poolPassword string, v Vars) Rewrite {
	user := strings.NewReplacer(
		"{worker}", Worker(login), "{login}", login,
		"{account}", v.Account, "{upstream_worker}", v.Worker,
	).Replace(userTemplate)
	pass := strings.ReplaceAll(poolPassword, "{password}", asicPassword)
	return Rewrite{User: user, Password: pass}
}
