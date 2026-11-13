package bus

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

type a2aRequestHandler struct {
	runtime       *Runtime
	publicationID string
	credential    string
}

var _ a2asrv.RequestHandler = (*a2aRequestHandler)(nil)

func (s *Server) serveA2A(response http.ResponseWriter, request *http.Request) {
	credential, _ := bearer(request)
	publicationID := request.PathValue("publicationId")
	handler := a2asrv.NewRESTHandler(&a2aRequestHandler{
		runtime: s.runtime, publicationID: publicationID, credential: credential,
	})

	forwarded := request.Clone(request.Context())
	forwarded.URL = new(url.URL)
	*forwarded.URL = *request.URL
	prefix := "/a2a/agents/" + publicationID
	forwarded.URL.Path = strings.TrimPrefix(request.URL.Path, prefix)
	if forwarded.URL.Path == "" {
		forwarded.URL.Path = "/"
	}
	forwarded.URL.RawPath = ""
	forwarded.Body = http.MaxBytesReader(response, request.Body, maxBodyBytes)
	handler.ServeHTTP(response, forwarded)
}

func (h *a2aRequestHandler) authorize(ctx context.Context) error {
	if _, err := h.runtime.AuthenticateA2APrincipal(ctx, h.credential, h.publicationID); err != nil {
		return a2a.NewError(a2a.ErrUnauthenticated, "Valid bearer credentials are required")
	}
	call, ok := a2asrv.CallContextFrom(ctx)
	if !ok {
		return a2a.NewError(a2a.ErrInternalError, "Request context is unavailable")
	}
	versions, present := call.ServiceParams().Get(a2a.SvcParamVersion)
	if present && (len(versions) != 1 || versions[0] != string(a2a.Version)) {
		return a2a.NewError(a2a.ErrVersionNotSupported, "A2A protocol version is not supported")
	}
	return nil
}

func (h *a2aRequestHandler) SendMessage(ctx context.Context, request *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	if err := h.authorize(ctx); err != nil {
		return nil, err
	}
	input, err := acceptA2AInput(request)
	if err != nil {
		return nil, err
	}
	task, err := h.runtime.AcceptA2AMessage(ctx, h.credential, h.publicationID, input)
	if err != nil {
		return nil, mapBusA2AError(err)
	}
	return taskToA2A(task, request.Message), nil
}

func acceptA2AInput(request *a2a.SendMessageRequest) (AcceptA2AMessageInput, error) {
	if request == nil || request.Message == nil {
		return AcceptA2AMessageInput{}, a2a.NewError(a2a.ErrInvalidParams, "A message is required")
	}
	message := request.Message
	if message.ID == "" {
		return AcceptA2AMessageInput{}, a2a.NewError(a2a.ErrInvalidParams, "messageId is required")
	}
	if message.Role != a2a.MessageRoleUser {
		return AcceptA2AMessageInput{}, a2a.NewError(a2a.ErrInvalidParams, "Only user messages are accepted")
	}
	if request.Config != nil && request.Config.PushConfig != nil {
		return AcceptA2AMessageInput{}, a2a.NewError(a2a.ErrUnsupportedOperation, "Push notifications are not supported")
	}
	if len(request.Metadata) != 0 || len(message.Extensions) != 0 || len(message.Metadata) != 0 || len(message.ReferenceTasks) != 0 {
		return AcceptA2AMessageInput{}, a2a.NewError(a2a.ErrUnsupportedOperation, "Message extensions and task references are not supported")
	}
	if len(message.Parts) == 0 {
		return AcceptA2AMessageInput{}, a2a.NewError(a2a.ErrInvalidParams, "At least one text part is required")
	}
	parts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		if part == nil {
			return AcceptA2AMessageInput{}, a2a.NewError(a2a.ErrInvalidParams, "Message parts cannot be null")
		}
		text, ok := part.Content.(a2a.Text)
		if !ok {
			return AcceptA2AMessageInput{}, a2a.NewError(a2a.ErrUnsupportedContentType, "Only text message parts are supported")
		}
		if part.Filename != "" || len(part.Metadata) != 0 || (part.MediaType != "" && part.MediaType != "text/plain") {
			return AcceptA2AMessageInput{}, a2a.NewError(a2a.ErrUnsupportedContentType, "Only plain text message parts are supported")
		}
		parts = append(parts, string(text))
	}
	return AcceptA2AMessageInput{
		TaskID: string(message.TaskID), ContextID: message.ContextID,
		ClientMessageID: message.ID, Body: strings.Join(parts, "\n"),
	}, nil
}

func a2aTaskState(state A2ATaskState) a2a.TaskState {
	switch state {
	case A2ATaskWorking:
		return a2a.TaskStateWorking
	case A2ATaskInputRequired:
		return a2a.TaskStateInputRequired
	case A2ATaskCompleted:
		return a2a.TaskStateCompleted
	case A2ATaskFailed:
		return a2a.TaskStateFailed
	case A2ATaskCanceled:
		return a2a.TaskStateCanceled
	case A2ATaskRejected:
		return a2a.TaskStateRejected
	default:
		return a2a.TaskStateSubmitted
	}
}

func taskToA2A(task A2ATaskCorrelation, message *a2a.Message) *a2a.Task {
	timestamp, _ := time.Parse(time.RFC3339Nano, task.UpdatedAt)
	return &a2a.Task{
		ID: a2a.TaskID(task.ID), ContextID: task.ContextID,
		Status: a2a.TaskStatus{State: a2aTaskState(task.State), Timestamp: &timestamp}, History: []*a2a.Message{message},
	}
}

// taskHistoryToA2A renders one user message per correlated turn in acceptance
// order, each followed by its agent reply when one exists. A negative
// historyLength keeps the full history; otherwise only the most recent
// historyLength messages are returned. Agent replies surface in history only;
// status carries the state and timestamp without a duplicate message.
func taskHistoryToA2A(task A2ATaskCorrelation, history []A2ATaskHistoryEntry, historyLength int) *a2a.Task {
	timestamp, _ := time.Parse(time.RFC3339Nano, task.UpdatedAt)
	messages := make([]*a2a.Message, 0, 2*len(history))
	for _, entry := range history {
		messages = append(messages, &a2a.Message{
			ID: entry.ClientMessageID, ContextID: task.ContextID, TaskID: a2a.TaskID(task.ID),
			Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.NewTextPart(entry.RequestBody)},
		})
		if entry.ResponseMessageID != "" {
			messages = append(messages, &a2a.Message{
				ID: entry.ResponseMessageID, ContextID: task.ContextID, TaskID: a2a.TaskID(task.ID),
				Role: a2a.MessageRoleAgent, Parts: a2a.ContentParts{a2a.NewTextPart(entry.ResponseBody)},
			})
		}
	}
	if historyLength >= 0 && historyLength < len(messages) {
		messages = messages[len(messages)-historyLength:]
	}
	return &a2a.Task{
		ID: a2a.TaskID(task.ID), ContextID: task.ContextID,
		Status: a2a.TaskStatus{State: a2aTaskState(task.State), Timestamp: &timestamp}, History: messages,
	}
}

func mapBusA2AError(err error) error {
	var busErr *BusError
	if !errors.As(err, &busErr) {
		return a2a.NewError(a2a.ErrInternalError, "The message could not be accepted")
	}
	switch busErr.Code {
	case CodeUnauthenticated:
		return a2a.NewError(a2a.ErrUnauthenticated, "Valid bearer credentials are required")
	case CodeInvalidArgument:
		return a2a.NewError(a2a.ErrInvalidParams, busErr.Message)
	case CodeNotFound:
		return a2a.NewError(a2a.ErrTaskNotFound, "Task not found")
	case CodeConflict:
		return a2a.NewError(a2a.ErrInvalidRequest, busErr.Message)
	case CodeBackpressure:
		return a2a.NewError(a2a.ErrServerError, "Remote work capacity is full")
	default:
		return a2a.NewError(a2a.ErrInternalError, "The message could not be accepted")
	}
}

func (h *a2aRequestHandler) unsupported(ctx context.Context) error {
	if err := h.authorize(ctx); err != nil {
		return err
	}
	return a2a.ErrUnsupportedOperation
}

func (h *a2aRequestHandler) GetTask(ctx context.Context, request *a2a.GetTaskRequest) (*a2a.Task, error) {
	if err := h.authorize(ctx); err != nil {
		return nil, err
	}
	if request == nil || request.ID == "" {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "A task ID is required")
	}
	historyLength := -1
	if request.HistoryLength != nil {
		if *request.HistoryLength < 0 {
			return nil, a2a.NewError(a2a.ErrInvalidParams, "historyLength cannot be negative")
		}
		historyLength = *request.HistoryLength
	}
	task, history, err := h.runtime.A2ATaskHistory(ctx, h.credential, h.publicationID, string(request.ID))
	if err != nil {
		return nil, mapBusA2AError(err)
	}
	return taskHistoryToA2A(task, history, historyLength), nil
}

func (h *a2aRequestHandler) ListTasks(ctx context.Context, _ *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	return nil, h.unsupported(ctx)
}

func (h *a2aRequestHandler) CancelTask(ctx context.Context, _ *a2a.CancelTaskRequest) (*a2a.Task, error) {
	return nil, h.unsupported(ctx)
}

func (h *a2aRequestHandler) SubscribeToTask(ctx context.Context, _ *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return a2aErrorSequence(h.unsupported(ctx))
}

func (h *a2aRequestHandler) SendStreamingMessage(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return a2aErrorSequence(h.unsupported(ctx))
}

func a2aErrorSequence(err error) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) { yield(nil, err) }
}

func (h *a2aRequestHandler) GetTaskPushConfig(ctx context.Context, _ *a2a.GetTaskPushConfigRequest) (*a2a.PushConfig, error) {
	return nil, h.unsupported(ctx)
}

func (h *a2aRequestHandler) ListTaskPushConfigs(ctx context.Context, _ *a2a.ListTaskPushConfigRequest) (*a2a.ListTaskPushConfigResponse, error) {
	return nil, h.unsupported(ctx)
}

func (h *a2aRequestHandler) CreateTaskPushConfig(ctx context.Context, _ *a2a.PushConfig) (*a2a.PushConfig, error) {
	return nil, h.unsupported(ctx)
}

func (h *a2aRequestHandler) DeleteTaskPushConfig(ctx context.Context, _ *a2a.DeleteTaskPushConfigRequest) error {
	return h.unsupported(ctx)
}

func (h *a2aRequestHandler) GetExtendedAgentCard(ctx context.Context, _ *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
	return nil, h.unsupported(ctx)
}
