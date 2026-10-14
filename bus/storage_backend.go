package bus

import "context"

type StorageBackend string

const StorageBackendSQLite StorageBackend = "sqlite"

type storageBackend interface {
	Backend() StorageBackend
	Ping(context.Context) error
	Close() error
	Backup(context.Context, string) error

	CreateScope(context.Context, string) (CreateScopeResult, error)
	ListScopes(context.Context) ([]ScopeInfo, error)
	RotateScopeToken(context.Context, string) (CreateScopeResult, error)
	DeleteScope(context.Context, string) (bool, error)
	AuthenticateScope(context.Context, string) (string, error)
	CredentialKind(context.Context, string) (CredentialKind, error)
	RegisterAgent(context.Context, string, RegisterAgentInput) (RegisterAgentResult, error)
	CurrentAgentToken(context.Context, string) (Principal, error)
	AuthenticateAgent(context.Context, string) (Principal, error)
	Agent(context.Context, string, string) (Agent, error)
	Heartbeat(context.Context, Principal, HeartbeatInput) (Agent, bool, bool, error)
	RetireAgent(context.Context, string) (Principal, error)
	ListAgents(context.Context, string) ([]Agent, error)
	LinkAgents(context.Context, string, string, string) error
	ListPeers(context.Context, Principal) ([]Agent, error)

	SendMessage(context.Context, Principal, SendMessageInput) (DeliveryReceipt, error)
	Receipt(context.Context, Principal, string) (DeliveryReceipt, error)
	ReserveInbox(context.Context, Principal, int) (*InboxReservation, error)
	NextInboxReservationExpiry(context.Context, Principal) (int64, error)
	CommitInbox(context.Context, Principal, string) ([]Message, error)
	ReleaseInbox(context.Context, Principal, string) error
	AcknowledgeMessages(context.Context, Principal, []string) (int64, error)

	AddTask(context.Context, string, string, AddTaskInput) (Task, error)
	AddAgentTask(context.Context, Principal, AddTaskInput) (Task, error)
	ClaimTask(context.Context, Principal, string) (Task, error)
	ReleaseTask(context.Context, Principal, string) (Task, error)
	CompleteTask(context.Context, Principal, string, string) (Task, error)
	ListTasks(context.Context, string, bool) ([]Task, error)
	TaskPage(context.Context, string, string, int) (TaskPage, error)
	AddTaskProgress(context.Context, Principal, string, AddTaskProgressInput) (TaskProgress, error)
	ListTaskProgress(context.Context, string, string) ([]TaskProgress, error)

	AskHuman(context.Context, Principal, AskHumanInput) (HumanEscalation, error)
	Escalation(context.Context, string, string) (HumanEscalation, error)
	ListEscalations(context.Context, string) ([]HumanEscalation, error)
	ResolveEscalation(context.Context, string, string, string) (HumanEscalation, error)

	StorageSummary(context.Context, string) (StorageSummary, error)
	PruneScope(context.Context, string, int64, bool) (PruneScopeResult, error)
	Events(context.Context, string, int64, int) (EventBatch, error)
	ExportScope(context.Context, string) (ScopeArchive, error)
	ImportScope(context.Context, ScopeArchive) (ImportScopeResult, error)

	CreateAgentCardPublication(context.Context, string, string) (agentCardPublication, error)
	ListAgentCardPublications(context.Context, string) ([]agentCardPublication, error)
	SetAgentCardPublicationEnabled(context.Context, string, string, bool) (agentCardPublication, error)
	PublishedAgent(context.Context, string) (agentCardPublication, Agent, error)
	CreateA2APrincipal(context.Context, string, CreateA2APrincipalInput) (IssuedA2APrincipal, error)
	ListA2APrincipals(context.Context, string) ([]A2APrincipal, error)
	ListA2APrincipalUsage(context.Context, string, A2APrincipalLimits) ([]A2APrincipalUsage, error)
	RotateA2APrincipal(context.Context, string, string) (IssuedA2APrincipal, error)
	SetA2APrincipalEnabled(context.Context, string, string, bool) (A2APrincipal, error)
	AuthenticateA2APrincipal(context.Context, string, string) (A2APrincipal, error)
	AcceptA2AMessage(context.Context, A2APrincipal, A2APrincipalLimits, AcceptA2AMessageInput) (A2ATaskCorrelation, error)
	A2ATask(context.Context, A2APrincipal, string) (A2ATaskCorrelation, error)

	CreateOutputStream(context.Context, string, CreateOutputStreamInput) (OutputStream, error)
	ListOutputStreams(context.Context, string) ([]OutputStream, error)
	OutputStream(context.Context, string, string) (OutputStream, error)
	RemoveOutputStream(context.Context, string, string) error
	SetOutputPublisher(context.Context, string, string, string, bool) (OutputStream, error)
	PublishOutput(context.Context, outputPublisher, string, PublishOutputInput, string, string) (OutputValue, string, error)
	readOutput(context.Context, string, string, string, int64, int, bool) (OutputHistory, *OutputValue, error)
	CreateOutputPrincipal(context.Context, string, CreateOutputPrincipalInput) (IssuedOutputPrincipal, error)
	ListOutputPrincipals(context.Context, string) ([]OutputPrincipal, error)
	RotateOutputPrincipal(context.Context, string, string) (IssuedOutputPrincipal, error)
	SetOutputPrincipalEnabled(context.Context, string, string, bool) (OutputPrincipal, error)
}

var _ storageBackend = (*Store)(nil)
