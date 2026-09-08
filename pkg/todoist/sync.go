package todoist

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// MaxCommandsPerRequest is the hard server-side cap on a /sync commands array.
const MaxCommandsPerRequest = 100

// Namespace is the fixed UUIDv5 namespace used to derive deterministic command
// uuids. Changing it would break idempotency against tasks already created, so
// it must never change.
var Namespace = uuid.MustParse("1f0a2b3c-4d5e-5f60-8a9b-0c1d2e3f4a5b")

// CommandUUID derives the idempotency key for creating a task from an issue.
// Todoist refuses to execute a command whose uuid already ran, so the same
// issue URL can be pushed repeatedly and only ever produces one task.
func CommandUUID(issueURL string) string {
	return uuid.NewSHA1(Namespace, []byte(issueURL)).String()
}

// CommandUUIDGen derives the create idempotency key for a given revive
// generation. Generation 0 is the first push and matches CommandUUID exactly.
//
// Reviving needs its own key: the whole point of the base uuid is that Todoist
// refuses to run it twice, so a revived issue would otherwise never get a new
// task. Bumping the generation on each revive keeps every push idempotent
// within its generation while still allowing a deliberate re-add.
func CommandUUIDGen(issueURL string, generation int) string {
	if generation <= 0 {
		return CommandUUID(issueURL)
	}
	return uuid.NewSHA1(Namespace, []byte(fmt.Sprintf("%s#revive#%d", issueURL, generation))).String()
}

// CloseCommandUUID derives the idempotency key for completing an issue's task.
// closedAt is mixed in so that closing a reopened-and-reclosed issue is not
// swallowed as a duplicate of the first close.
func CloseCommandUUID(issueURL, closedAt string) string {
	return uuid.NewSHA1(Namespace, []byte(issueURL+"#close#"+closedAt)).String()
}

// TempID derives the temp_id for an item_add. It only needs to be unique within
// the request, but deriving it keeps requests reproducible.
func TempID(issueURL string, generation int) string {
	return uuid.NewSHA1(Namespace, []byte(fmt.Sprintf("temp:%s#%d", issueURL, generation))).String()
}

// Due is the due-date object accepted by item_add.
type Due struct {
	Date string `json:"date"`
}

// ItemAddArgs are the arguments of an item_add command.
type ItemAddArgs struct {
	Content     string   `json:"content"`
	Description string   `json:"description,omitempty"`
	ProjectID   string   `json:"project_id,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Priority    int      `json:"priority,omitempty"`
	Due         *Due     `json:"due,omitempty"`
}

// Command is a single entry of the /sync commands array.
type Command struct {
	Type   string `json:"type"`
	UUID   string `json:"uuid"`
	TempID string `json:"temp_id,omitempty"`
	Args   any    `json:"args"`
}

// NewItemAdd builds an item_add command whose uuid is derived from issueURL and
// the revive generation.
func NewItemAdd(issueURL string, generation int, args ItemAddArgs) Command {
	return Command{
		Type:   "item_add",
		UUID:   CommandUUIDGen(issueURL, generation),
		TempID: TempID(issueURL, generation),
		Args:   args,
	}
}

// NewItemClose builds an item_close command for an existing task id.
func NewItemClose(issueURL, closedAt, taskID string) Command {
	return Command{
		Type: "item_close",
		UUID: CloseCommandUUID(issueURL, closedAt),
		Args: map[string]string{"id": taskID},
	}
}

// CommandError is a per-command failure reported in sync_status.
type CommandError struct {
	Error     string `json:"error"`
	ErrorCode int    `json:"error_code"`
	ErrorTag  string `json:"error_tag"`
}

// SyncResult is the outcome of one or more /sync requests, merged.
type SyncResult struct {
	// Status maps command uuid to nil on success or the reported error.
	Status map[string]*CommandError
	// TempIDMapping maps temp_id to the real task id, for commands that
	// actually executed. An already-executed uuid may be absent.
	TempIDMapping map[string]string
}

// OK reports whether the command with this uuid succeeded.
func (r SyncResult) OK(cmdUUID string) bool {
	err, present := r.Status[cmdUUID]
	return present && err == nil
}

// Err returns the failure for a command uuid, or nil.
func (r SyncResult) Err(cmdUUID string) *CommandError {
	return r.Status[cmdUUID]
}

// syncResponse is the raw wire shape. sync_status values are either the string
// "ok" or an error object, so they are decoded lazily.
type syncResponse struct {
	SyncStatus    map[string]json.RawMessage `json:"sync_status"`
	TempIDMapping map[string]any             `json:"temp_id_mapping"`
}

// Sync submits commands to POST /sync, chunked at MaxCommandsPerRequest.
//
// Batches are not transactional: a partial failure returns a populated
// SyncResult alongside a nil error, and callers must inspect every uuid.
// A non-nil error means an entire request failed at the transport or HTTP
// level; the SyncResult still carries whatever earlier chunks reported.
func (c *Client) Sync(ctx context.Context, commands []Command) (SyncResult, error) {
	result := SyncResult{
		Status:        map[string]*CommandError{},
		TempIDMapping: map[string]string{},
	}
	for start := 0; start < len(commands); start += MaxCommandsPerRequest {
		end := min(start+MaxCommandsPerRequest, len(commands))
		if err := c.syncChunk(ctx, commands[start:end], &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (c *Client) syncChunk(ctx context.Context, commands []Command, result *SyncResult) error {
	encoded, err := json.Marshal(commands)
	if err != nil {
		return fmt.Errorf("encode sync commands: %w", err)
	}
	form := url.Values{}
	form.Set("commands", string(encoded))

	var resp syncResponse
	err = c.do(ctx, "POST", "/sync", nil,
		[]byte(form.Encode()), "application/x-www-form-urlencoded", &resp)
	if err != nil {
		return fmt.Errorf("post sync (%d commands): %w", len(commands), err)
	}

	for id, raw := range resp.SyncStatus {
		result.Status[id] = decodeStatus(raw)
	}
	for tempID, real := range resp.TempIDMapping {
		result.TempIDMapping[tempID] = stringifyID(real)
	}
	return nil
}

// decodeStatus turns one sync_status value into nil (success) or an error.
func decodeStatus(raw json.RawMessage) *CommandError {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.EqualFold(s, "ok") {
			return nil
		}
		return &CommandError{Error: s}
	}
	var cmdErr CommandError
	if err := json.Unmarshal(raw, &cmdErr); err != nil {
		return &CommandError{Error: string(raw)}
	}
	if cmdErr.Error == "" {
		cmdErr.Error = string(raw)
	}
	return &cmdErr
}

// stringifyID normalises an id that may arrive as a JSON string or number.
func stringifyID(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%.0f", t)
	default:
		return fmt.Sprint(t)
	}
}
