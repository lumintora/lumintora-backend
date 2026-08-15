// Package tenant maps an authenticated user id to their private Postgres schema.
//
// Lumintora uses schema-per-tenant: each user's learning data (learning_paths,
// modules, quiz_questions, user_module_progress, xp_transactions, path_adaptations)
// lives in a dedicated schema whose name is a short alphanumeric id (e.g.
// "a1b2c3d4e5"). Global tables (users, certificates, feedback, waitlist,
// career_applications) stay in public and are referenced unqualified. Handlers
// qualify per-tenant tables with Schema(...).
package tenant

import (
	"context"
	"database/sql"
	"regexp"
	"sync"
)

// uuidRe validates the user id before it is used as a lookup key.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// keyRe validates a tenant_key read from the DB before it is interpolated into
// SQL as a schema identifier. The leading-letter + lowercase-alphanumeric shape
// matches what gen_tenant_key() produces and keeps the interpolation
// injection-safe (identifiers can't be bound as parameters).
var keyRe = regexp.MustCompile(`^[a-z][a-z0-9]{9}$`)

// cache memoizes userID -> schema name. A user's tenant_key is assigned once at
// signup and never changes, so entries are valid for the process lifetime and
// never need invalidation.
var cache sync.Map

// Schema returns the tenant schema name for a user id (e.g. "a1b2c3d4e5"), and
// false if the id is malformed or the user has no provisioned tenant.
//
// The schema name lives in users.tenant_key (assigned by create_tenant_schema
// at signup), so the first call for a user does one small indexed lookup; every
// later call is served from the in-process cache.
func Schema(ctx context.Context, db *sql.DB, userID string) (string, bool) {
	if !uuidRe.MatchString(userID) {
		return "", false
	}
	if v, ok := cache.Load(userID); ok {
		return v.(string), true
	}
	var key string
	if err := db.QueryRowContext(ctx, `SELECT tenant_key FROM users WHERE id=$1`, userID).Scan(&key); err != nil {
		return "", false
	}
	if !keyRe.MatchString(key) {
		return "", false
	}
	cache.Store(userID, key)
	return key, true
}

// CreateSchemaSQL provisions a user's schema and tables via the idempotent
// create_tenant_schema() function, assigning a tenant_key if the user has none
// yet. auth-service runs it right after inserting a new user. Safe to call more
// than once.
const CreateSchemaSQL = `SELECT create_tenant_schema($1)`
