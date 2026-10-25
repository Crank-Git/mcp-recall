// mcprecall-go - Developed by acidvegas in Go (https://github.com/acidvegas)
// internal/db/types.go

package db

// StoredOutput is a fully-hydrated row from the stored_outputs table.
type StoredOutput struct {
	ID           string
	ProjectKey   string
	SessionID    string
	ToolName     string
	Summary      string
	FullContent  string
	OriginalSize int
	SummarySize  int
	CreatedAt    int64
	Pinned       int // 0 | 1
	AccessCount  int
	LastAccessed *int64
	InputHash    *string
	OutputHash   *string
	// FullRetained is 1 when the verbatim body is persisted (retrievable), 0 when
	// the row is summary-only (body dropped per store.retention).
	FullRetained int
	// CommandFP is the privacy-safe command family fingerprint (Bash rows only;
	// nil otherwise or pre-migration). See upstream #251.
	CommandFP *string
	// DeliveredSize is the bytes Claude received: header, newline, summary
	// (upstream #319). nil for notes and pre-migration rows.
	DeliveredSize *int64
}

// StoreInput is the input required to persist a new compressed tool output.
type StoreInput struct {
	ProjectKey   string
	SessionID    string
	ToolName     string
	Summary      string
	FullContent  string
	OriginalSize int
	InputHash    *string
	// OutputHash is the precomputed sha256 of FullContent; StoreOutput derives
	// it when nil.
	OutputHash *string
	// FullRetained nil (default) persists the verbatim body and chunks it for
	// retrieval; 0 stores the row summary-only — FullContent is not persisted and
	// no chunks are written, but OutputHash is still derived from the real
	// content so dedup keeps working.
	FullRetained *int
	// CommandFP is the privacy-safe command family fingerprint (Bash only); nil
	// stores NULL.
	CommandFP *string
	// DeliveredSize is the bytes Claude received for this output (upstream
	// #319); nil stores NULL.
	DeliveredSize *int64
}

// SearchOptions configures full-text search across stored outputs.
type SearchOptions struct {
	ProjectKey string
	Tool       string // "" = any
	Limit      int    // 0 = default (10)
}

// ListOptions configures paginated listing of stored outputs.
type ListOptions struct {
	ProjectKey string
	Tool       string // "" = any
	Limit      int    // 0 = default (20)
	Offset     int
	Sort       string // "newest" (default) | "oldest"
}

// ForgetOptions selects stored outputs to delete. Exactly one selector should be set.
type ForgetOptions struct {
	ID            string
	Tool          string
	SessionID     string
	OlderThanDays *int
	All           bool
	Force         bool // override pinned protection
}

// Stats holds aggregate storage statistics for a project.
type Stats struct {
	// Total* and CompressionRatio cover intercepted output only — recall__note
	// memory is excluded so it never dilutes the savings figure.
	TotalItems         int
	TotalOriginalBytes int
	TotalSummaryBytes  int
	CompressionRatio   float64
	// Pinned* are store-wide (notes included): pinned bytes are what eviction
	// cannot reclaim, whatever wrote them. PinnedBytes is effective size
	// (summary_size for summary-only rows), matching the max_pinned_mb budget.
	PinnedItems int
	PinnedBytes int
	// Note* are recall__note memory — stored memory, not interception.
	NoteItems int
	NoteBytes int
}

// ToolBreakdownRow is a per-tool storage stats row.
type ToolBreakdownRow struct {
	ToolName      string
	Items         int
	OriginalBytes int
	SummaryBytes  int
}

// CommandBreakdownRow is a per-command-family row from GetBashCommandBreakdown.
type CommandBreakdownRow struct {
	CommandFP     string // "unknown" for pre-migration / untagged Bash rows
	Items         int
	OriginalBytes int
	SummaryBytes  int
}

// SuggestionsOptions configures GetSuggestions.
type SuggestionsOptions struct {
	PinThreshold int // 0 = default (5)
	StaleDays    int // 0 = default (3)
	Limit        int // 0 = default (3)
}

// SuggestionsData holds pin and stale candidate lists.
type SuggestionsData struct {
	PinCandidates   []StoredOutput
	StaleCandidates []StoredOutput
}

// LastSession is the last-session headline within ContextData.
type LastSession struct {
	Date               string
	StoredCount        int
	TotalOriginalBytes int
	TotalSummaryBytes  int
}

// ContextData is the session-orientation snapshot in five isolated sections.
type ContextData struct {
	Pinned      SessionSample
	Notes       []StoredOutput
	Recent      []StoredOutput
	Hot         []StoredOutput
	LastSession *LastSession
}

// SessionSummaryOptions filters GetSessionSummary. Provide session_id or date.
type SessionSummaryOptions struct {
	SessionID string
	Date      string // YYYY-MM-DD, defaults to today (UTC)
}

// ToolCount is a per-tool count within a session summary.
type ToolCount struct {
	ToolName string
	Count    int
}

// TopAccessed is a most-accessed item within a session summary.
type TopAccessed struct {
	ID          string
	ToolName    string
	Summary     string
	AccessCount int
}

// SampledItem is a sampled row: display columns only, no full_content.
type SampledItem struct {
	ID           string
	SessionID    string
	ToolName     string
	Summary      string
	CreatedAt    int64
	LastAccessed *int64
}

// SessionOverflow counts the rows of one session left out of a sample.
type SessionOverflow struct {
	SessionID string
	Count     int
}

// SessionSample is a bounded, session-fair sample of a row set that a bulk
// writer can grow to thousands (upstream #269, #336). Total is the real count;
// Overflow counts the rows left out of Items per session, largest first.
type SessionSample struct {
	Items    []SampledItem
	Total    int
	Overflow []SessionOverflow
}

// SessionSummaryData is the digest returned by GetSessionSummary.
type SessionSummaryData struct {
	Label              string
	StoredCount        int
	TotalOriginalBytes int
	TotalSummaryBytes  int
	ToolCounts         []ToolCount
	AccessedCount      int
	TotalAccesses      int
	TopAccessed        []TopAccessed
	Pinned             SessionSample
	Notes              SessionSample
}
