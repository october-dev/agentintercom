package bus

const ProtocolVersion = "0.1"

var Version = "dev"

type AgentLifecycle string

const (
	LifecycleStarting   AgentLifecycle = "starting"
	LifecycleReady      AgentLifecycle = "ready"
	LifecycleWorking    AgentLifecycle = "working"
	LifecycleIdle       AgentLifecycle = "idle"
	LifecycleNeedsInput AgentLifecycle = "needs_input"
	LifecycleOffline    AgentLifecycle = "offline"
)

type MessageMode string

const (
	MessageNotify   MessageMode = "notify"
	MessageRequest  MessageMode = "request"
	MessageResponse MessageMode = "response"
)

type MessageParticipantKind string

const (
	MessageParticipantAgent        MessageParticipantKind = "agent"
	MessageParticipantHuman        MessageParticipantKind = "human"
	MessageParticipantA2APrincipal MessageParticipantKind = "a2aPrincipal"
)

type DeliveryState string

const (
	DeliveryQueued       DeliveryState = "queued"
	DeliveryReserved     DeliveryState = "reserved"
	DeliveryDelivered    DeliveryState = "delivered"
	DeliveryAcknowledged DeliveryState = "acknowledged"
	DeliveryExpired      DeliveryState = "expired"
)

type AgentCapability struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type AgentIdentity struct {
	ScopeID     string `json:"scopeId"`
	AgentID     string `json:"agentId"`
	ExecutionID string `json:"executionId"`
}

type Agent struct {
	ID           string            `json:"id"`
	DisplayName  string            `json:"displayName"`
	Capabilities []AgentCapability `json:"capabilities"`
	Lifecycle    AgentLifecycle    `json:"lifecycle"`
	Ready        bool              `json:"ready"`
	Reachable    bool              `json:"reachable"`
	ExecutionID  string            `json:"executionId"`
	RegisteredAt string            `json:"registeredAt"`
	UpdatedAt    string            `json:"updatedAt"`
}

type ContextItem struct {
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Text      string `json:"text,omitempty"`
	URI       string `json:"uri,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
}

type Message struct {
	ID                string                 `json:"id"`
	ScopeID           string                 `json:"scopeId"`
	From              string                 `json:"from"`
	FromKind          MessageParticipantKind `json:"fromKind"`
	To                string                 `json:"to"`
	ToKind            MessageParticipantKind `json:"toKind"`
	Mode              MessageMode            `json:"mode"`
	Body              string                 `json:"body"`
	Context           []ContextItem          `json:"context"`
	ResponseTo        string                 `json:"responseTo,omitempty"`
	State             DeliveryState          `json:"state"`
	CreatedAt         string                 `json:"createdAt"`
	ExpiresAt         string                 `json:"expiresAt,omitempty"`
	DeliveredAt       string                 `json:"deliveredAt,omitempty"`
	AcknowledgedAt    string                 `json:"acknowledgedAt,omitempty"`
	RepliedAt         string                 `json:"repliedAt,omitempty"`
	ResponseMessageID string                 `json:"responseMessageId,omitempty"`
}

type A2ATaskState string

const (
	A2ATaskSubmitted     A2ATaskState = "submitted"
	A2ATaskWorking       A2ATaskState = "working"
	A2ATaskInputRequired A2ATaskState = "input-required"
	A2ATaskCompleted     A2ATaskState = "completed"
	A2ATaskFailed        A2ATaskState = "failed"
	A2ATaskCanceled      A2ATaskState = "canceled"
	A2ATaskRejected      A2ATaskState = "rejected"
)

type A2AMessageCorrelation struct {
	ClientMessageID      string `json:"clientMessageId"`
	BusRequestMessageID  string `json:"busRequestMessageId"`
	BusResponseMessageID string `json:"busResponseMessageId,omitempty"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
}

type A2ATaskCorrelation struct {
	ID            string                  `json:"id"`
	ContextID     string                  `json:"contextId"`
	PrincipalID   string                  `json:"principalId"`
	PublicationID string                  `json:"publicationId"`
	TargetAgentID string                  `json:"targetAgentId"`
	State         A2ATaskState            `json:"state"`
	Messages      []A2AMessageCorrelation `json:"messages"`
	CreatedAt     string                  `json:"createdAt"`
	UpdatedAt     string                  `json:"updatedAt"`
}

type AcceptA2AMessageInput struct {
	TaskID          string `json:"taskId,omitempty"`
	ContextID       string `json:"contextId,omitempty"`
	ClientMessageID string `json:"clientMessageId"`
	Body            string `json:"body"`
}

type A2APrincipalLimits struct {
	MessageLimit int64 `json:"messageLimit"`
	ByteLimit    int64 `json:"byteLimit"`
}

type A2APrincipalUsage struct {
	PrincipalID        string `json:"principalId"`
	PublicationID      string `json:"publicationId"`
	UnfinishedMessages int64  `json:"unfinishedMessages"`
	UnfinishedBytes    int64  `json:"unfinishedBytes"`
	MessageLimit       int64  `json:"messageLimit"`
	ByteLimit          int64  `json:"byteLimit"`
}

type DeliveryReceipt struct {
	MessageID         string        `json:"messageId"`
	State             DeliveryState `json:"state"`
	AcceptedAt        string        `json:"acceptedAt"`
	DeliveredAt       string        `json:"deliveredAt,omitempty"`
	AcknowledgedAt    string        `json:"acknowledgedAt,omitempty"`
	RepliedAt         string        `json:"repliedAt,omitempty"`
	ResponseMessageID string        `json:"responseMessageId,omitempty"`
}

type InboxReservation struct {
	ID        string    `json:"id"`
	ExpiresAt string    `json:"expiresAt"`
	Messages  []Message `json:"messages"`
}

type Task struct {
	ID             string         `json:"id"`
	ScopeID        string         `json:"scopeId"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	CreatedBy      *string        `json:"createdBy"`
	ClaimedBy      string         `json:"claimedBy,omitempty"`
	Status         string         `json:"status"`
	Dependencies   []string       `json:"dependencies"`
	Ready          bool           `json:"ready"`
	RecentProgress []TaskProgress `json:"recentProgress"`
	Note           string         `json:"note,omitempty"`
	CreatedAt      string         `json:"createdAt"`
	UpdatedAt      string         `json:"updatedAt"`
}

type TaskProgress struct {
	TaskID      string `json:"taskId"`
	Sequence    int64  `json:"sequence"`
	AgentID     string `json:"agentId"`
	ExecutionID string `json:"executionId"`
	Kind        string `json:"kind"`
	Text        string `json:"text"`
	CreatedAt   string `json:"createdAt"`
}

type BusEvent struct {
	ID         string            `json:"id"`
	ScopeID    string            `json:"scopeId"`
	Type       string            `json:"type"`
	SubjectID  string            `json:"subjectId"`
	Revision   int64             `json:"revision"`
	Attributes map[string]string `json:"attributes"`
	CreatedAt  string            `json:"createdAt"`
}

type EventBatch struct {
	ScopeID         string     `json:"scopeId"`
	Events          []BusEvent `json:"events"`
	NextRevision    int64      `json:"nextRevision"`
	CurrentRevision int64      `json:"currentRevision"`
	MinimumCursor   int64      `json:"minimumCursor"`
	ResyncRequired  bool       `json:"resyncRequired"`
}

type AgentCardPublication struct {
	ID           string `json:"id"`
	ScopeID      string `json:"scopeId"`
	AgentID      string `json:"agentId"`
	Enabled      bool   `json:"enabled"`
	CardURL      string `json:"cardUrl"`
	InterfaceURL string `json:"interfaceUrl"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

type PublishAgentCardInput struct {
	AgentID string `json:"agentId"`
}

type A2APrincipal struct {
	tokenHash     string
	ID            string `json:"id"`
	ScopeID       string `json:"scopeId"`
	PublicationID string `json:"publicationId"`
	Label         string `json:"label"`
	Enabled       bool   `json:"enabled"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

type CreateA2APrincipalInput struct {
	PublicationID string `json:"publicationId"`
	Label         string `json:"label"`
}

type IssuedA2APrincipal struct {
	Principal  A2APrincipal `json:"principal"`
	Credential string       `json:"credential"`
}

type OutputContentType string

const (
	OutputText OutputContentType = "text/plain"
	OutputJSON OutputContentType = "application/json"
)

type OutputPermission string

const (
	OutputRead    OutputPermission = "read"
	OutputPublish OutputPermission = "publish"
)

type OutputReference struct {
	URI   string `json:"uri"`
	Title string `json:"title,omitempty"`
}

type OutputStream struct {
	ID                string   `json:"id"`
	ScopeID           string   `json:"scopeId"`
	Name              string   `json:"name"`
	RetentionLimit    int      `json:"retentionLimit"`
	CurrentSequence   int64    `json:"currentSequence"`
	MinimumCursor     int64    `json:"minimumCursor"`
	PublisherAgentIDs []string `json:"publisherAgentIds"`
	CreatedAt         string   `json:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"`
}

type CreateOutputStreamInput struct {
	Name              string   `json:"name"`
	RetentionLimit    int      `json:"retentionLimit,omitempty"`
	PublisherAgentIDs []string `json:"publisherAgentIds,omitempty"`
}

type PublishOutputInput struct {
	ContentType OutputContentType `json:"contentType"`
	Value       any               `json:"value"`
	Reference   *OutputReference  `json:"reference,omitempty"`
}

type OutputValue struct {
	StreamID     string            `json:"streamId"`
	Sequence     int64             `json:"sequence"`
	ProducerType string            `json:"producerType"`
	ProducerID   string            `json:"producerId"`
	ContentType  OutputContentType `json:"contentType"`
	Value        any               `json:"value"`
	Reference    *OutputReference  `json:"reference,omitempty"`
	CreatedAt    string            `json:"createdAt"`
}

type OutputHistory struct {
	StreamID        string        `json:"streamId"`
	Values          []OutputValue `json:"values"`
	NextSequence    int64         `json:"nextSequence"`
	CurrentSequence int64         `json:"currentSequence"`
	MinimumCursor   int64         `json:"minimumCursor"`
	ResyncRequired  bool          `json:"resyncRequired"`
}

type OutputPrincipal struct {
	ID          string             `json:"id"`
	ScopeID     string             `json:"scopeId"`
	StreamID    string             `json:"streamId"`
	Label       string             `json:"label"`
	Permissions []OutputPermission `json:"permissions"`
	Enabled     bool               `json:"enabled"`
	CreatedAt   string             `json:"createdAt"`
	UpdatedAt   string             `json:"updatedAt"`
}

type CreateOutputPrincipalInput struct {
	StreamID    string             `json:"streamId"`
	Label       string             `json:"label"`
	Permissions []OutputPermission `json:"permissions"`
}

type IssuedOutputPrincipal struct {
	Principal  OutputPrincipal `json:"principal"`
	Credential string          `json:"credential"`
}

type StorageRecordSummary struct {
	RecordType     string `json:"recordType"`
	State          string `json:"state"`
	Count          int64  `json:"count"`
	EstimatedBytes int64  `json:"estimatedBytes"`
	OldestAt       string `json:"oldestAt,omitempty"`
}

type StorageSummary struct {
	ScopeID             string                 `json:"scopeId"`
	GeneratedAt         string                 `json:"generatedAt"`
	TotalEstimatedBytes int64                  `json:"totalEstimatedBytes"`
	Records             []StorageRecordSummary `json:"records"`
}

type PruneScopeInput struct {
	Before  string `json:"before"`
	Execute bool   `json:"execute,omitempty"`
}

type RetentionCounts struct {
	A2ATasks     int64 `json:"a2aTasks"`
	A2AMessages  int64 `json:"a2aMessages"`
	Messages     int64 `json:"messages"`
	Tasks        int64 `json:"tasks"`
	TaskProgress int64 `json:"taskProgress"`
	Escalations  int64 `json:"escalations"`
	Events       int64 `json:"events"`
}

type PruneScopeResult struct {
	ScopeID string          `json:"scopeId"`
	Before  string          `json:"before"`
	DryRun  bool            `json:"dryRun"`
	Records RetentionCounts `json:"records"`
}

type HumanEscalation struct {
	ID         string   `json:"id"`
	ScopeID    string   `json:"scopeId"`
	AgentID    string   `json:"agentId"`
	Question   string   `json:"question"`
	Options    []string `json:"options"`
	Status     string   `json:"status"`
	Answer     string   `json:"answer,omitempty"`
	CreatedAt  string   `json:"createdAt"`
	ResolvedAt string   `json:"resolvedAt,omitempty"`
}

type CreateScopeInput struct {
	ID string `json:"id,omitempty"`
}

type CreateScopeResult struct {
	ScopeID    string `json:"scopeId"`
	ScopeToken string `json:"scopeToken"`
}

type RegisterAgentInput struct {
	ID               string            `json:"id,omitempty"`
	DisplayName      string            `json:"displayName"`
	Capabilities     []AgentCapability `json:"capabilities,omitempty"`
	ConnectTo        []string          `json:"connectTo,omitempty"`
	ConnectWorkspace bool              `json:"connectWorkspace,omitempty"`
	LeaseMS          int64             `json:"leaseMs,omitempty"`
}

type RegisterAgentResult struct {
	AgentIdentity
	AgentToken     string `json:"agentToken"`
	LeaseExpiresAt string `json:"leaseExpiresAt"`
}

type HeartbeatInput struct {
	Lifecycle AgentLifecycle `json:"lifecycle"`
	Ready     bool           `json:"ready"`
	LeaseMS   int64          `json:"leaseMs,omitempty"`
}

type SendMessageInput struct {
	To             string        `json:"to"`
	Body           string        `json:"body"`
	Mode           MessageMode   `json:"mode,omitempty"`
	ResponseTo     string        `json:"responseTo,omitempty"`
	IdempotencyKey string        `json:"idempotencyKey,omitempty"`
	ExpiresInMS    int64         `json:"expiresInMs,omitempty"`
	Context        []ContextItem `json:"context,omitempty"`
}

type AddTaskInput struct {
	Title        string   `json:"title"`
	Description  string   `json:"description,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
}

type AddTaskProgressInput struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type AskHumanInput struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

type RunFile struct {
	ProtocolVersion string `json:"protocolVersion"`
	Address         string `json:"address"`
	PID             int    `json:"pid"`
	StartedAt       string `json:"startedAt"`
	AdminToken      string `json:"adminToken"`
}

type Health struct {
	Name            string        `json:"name"`
	ProtocolVersion string        `json:"protocolVersion"`
	RuntimeVersion  string        `json:"runtimeVersion"`
	Status          string        `json:"status"`
	StartedAt       string        `json:"startedAt"`
	Storage         StorageHealth `json:"storage"`
	Features        []string      `json:"features,omitempty"`
}

// FeatureSessionRetirement includes idempotent retirement and obligation release.
const FeatureSessionRetirement = "session-retirement"

// FeatureNativeWake includes durable native wake attempts and status.
const FeatureNativeWake = "native-wake"

const (
	StorageAvailable   = "available"
	StorageUnavailable = "unavailable"
)

type StorageHealth struct {
	Backend StorageBackend `json:"backend"`
	Status  string         `json:"status"`
}

type Liveness struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	StartedAt string `json:"startedAt"`
}

type NodeIdentity struct {
	AgentIdentity
	LeaseExpiresAt string `json:"leaseExpiresAt"`
}

type NodeStatus struct {
	Identity NodeIdentity `json:"identity"`
	Agent    Agent        `json:"agent"`
}

// Credential kinds reported by GET /v1/credential.
const (
	CredentialScope     = "scope"
	CredentialAgent     = "agent"
	CredentialPrincipal = "principal"
)

// Credential classifies a bearer credential without authorizing anything.
type Credential struct {
	Kind string `json:"kind"`
}
