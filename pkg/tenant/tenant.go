// Package tenant maps an authenticated user id to their private Postgres schema.
//
// Lumintora uses schema-per-tenant: each user's learning data (learning_paths,
// modules, quiz_questions, user_module_progress, xp_transactions, path_adaptations)
// lives in a dedicated schema named tenant_<uuid-hex>. Global tables (users,
// certificates, feedback, waitlist, career_applications) stay in public and are
// referenced unqualified. Handlers qualify per-tenant tables with Schema(userID).
package tenant

import (
	"regexp"
	"strings"
)

// uuidRe validates the user id before it is interpolated into a schema name.
// Because Schema's result is concatenated into SQL (identifiers can't be bound
// as parameters), this guard is what keeps that interpolation injection-safe.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Schema returns the tenant schema name for a user id (e.g.
// "tenant_11111111111111111111111111111111"), and false if the id is not a
// well-formed UUID.
func Schema(userID string) (string, bool) {
	if !uuidRe.MatchString(userID) {
		return "", false
	}
	return "tenant_" + strings.ReplaceAll(userID, "-", ""), true
}

// CreateSchemaSQL provisions a user's schema and tables via the idempotent
// create_tenant_schema() function. auth-service runs it right after inserting a
// new user. Safe to call more than once.
const CreateSchemaSQL = `SELECT create_tenant_schema($1)`
