module odysseus

go 1.24.0

// Phase 2: bcrypt for password hashing, zxcvbn for password scoring
// Phase 3: mattn/go-sqlite3 for SQLite database access (cgo)
// Phase 5 will add: Friday LLM providers (copied, not imported)

require (
	github.com/mattn/go-sqlite3 v1.14.28
	github.com/nbutton23/zxcvbn-go v0.0.0-20210217022336-fa2cb2858354
	golang.org/x/crypto v0.38.0
)

require github.com/google/uuid v1.6.0
