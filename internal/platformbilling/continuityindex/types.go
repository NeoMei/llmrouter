// Package continuityindex holds private immutable ownership bindings for native
// provider history. It neither authenticates callers nor grants model access.
package continuityindex

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

var (
	ErrUnavailable = errors.New("native_history_unavailable")
	ErrInvalid     = errors.New("invalid native history completion")
	ErrConflict    = errors.New("native history binding conflict")
	ErrCapacity    = errors.New("native history index capacity reached")
	ErrStorage     = errors.New("native history index unavailable")
	ErrConfig      = errors.New("invalid native history index configuration")
)

type CredentialMode string

const (
	TaskCredential CredentialMode = "task"
	DurableKey     CredentialMode = "key"
)

type ResourceKind string

const (
	Response   ResourceKind = "response"
	OutputItem ResourceKind = "output_item"
)

type StoragePolicy string

const (
	ExplicitStored         StoragePolicy = "explicit_true"
	CertifiedDefaultStored StoragePolicy = "certified_default"
)

type Owner struct {
	ActorID, AccountID, ProductID, InstanceID string
	Mode                                      CredentialMode
	CredentialID, RootTaskID                  string
}
type Target struct {
	ChannelID                                    int64
	MemberID, PoolID, Namespace, APIOrganization string
}
type Provenance struct {
	CallID                                                             string
	AttemptID                                                          uint64
	PublicModel, UpstreamModel, Wire, ObservationDigest, PolicyVersion string
}

// TrustedCompletion is server-private evidence. A successful raw terminal must
// already have been validated; the callback must match the actual Call/Attempt.
// Only newly produced output IDs from stored responses.create belong here.
type TrustedCompletion struct {
	Owner                Owner
	Target               Target
	Provenance           Provenance
	Completed            bool
	Storage              StoragePolicy
	ResponseID           string
	OutputItemIDs        []string
	CreatedAt, ExpiresAt time.Time
}
type Selector struct {
	Kind ResourceKind
	ID   string
}

// Binding contains no raw resource ID, namespace, organization or owner IDs.
// RootTaskID is intentionally recoverable for verified exact-key continuation.
type Binding struct {
	ReferenceKey, SelectorDigest, OwnerDigest, RootDigest, KeyVersion string
	RootTaskID                                                        string
	Kind                                                              ResourceKind
	ResponseKey                                                       string
	ChannelID                                                         int64
	MemberID, PoolID, NamespaceDigest                                 string
	Provenance                                                        Provenance
	Storage                                                           StoragePolicy
	CreatedAt, ExpiresAt                                              time.Time
}
type Resolution struct {
	// Target is the actual trusted send target returned by ValidateCreator; it is
	// an admission input, not current eligibility or destination-model authority.
	Target     Target
	RootTaskID string
	Bindings   []Binding
}
type Config struct {
	ActiveKeyVersion string
	Keys             map[string][]byte
	TTL              time.Duration
	Capacity         int64
	Now              func() time.Time
	// ValidateCreator MUST verify the actual durable Call and sent Attempt against
	// the supplied owner and binding. It is also called on reads, before any grant.
	// Return the actual sent target loaded from that record; the index verifies
	// its namespace commitment itself, including across retained key versions.
	// It must not perform paid IO or call back into this index while it holds a gate.
	ValidateCreator func(context.Context, Owner, Binding) (Target, error)
}
type Index struct {
	db     *gorm.DB
	config Config
}
