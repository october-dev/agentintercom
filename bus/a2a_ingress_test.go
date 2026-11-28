package bus

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

func a2aTestTransport(t *testing.T, server *httptest.Server, publicationID string) a2aclient.Transport {
	t.Helper()
	endpoint, err := url.Parse(server.URL + "/a2a/agents/" + publicationID)
	requireNoError(t, err)
	return a2aclient.NewRESTTransport(endpoint, server.Client())
}

func a2aTestParams(credential, version string) a2aclient.ServiceParams {
	params := a2aclient.ServiceParams{"Authorization": []string{"Bearer " + credential}}
	if version != "" {
		params[a2a.SvcParamVersion] = []string{version}
	}
	return params
}

func TestA2ASendMessageCreatesDurableBusRequest(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	publication, issued := setupA2APrincipal(t, agents)
	server := httptest.NewServer(NewServer(agents.runtime, ServerOptions{}))
	defer server.Close()
	transport := a2aTestTransport(t, server, publication.ID)
	defer transport.Destroy()

	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Review this"), a2a.NewTextPart("Focus on security"))
	message.ID = "remote-message-1"
	result, err := transport.SendMessage(context.Background(), a2aTestParams(issued.Credential, string(a2a.Version)), &a2a.SendMessageRequest{Message: message})
	requireNoError(t, err)
	task, ok := result.(*a2a.Task)
	require(t, ok && task.ID != "" && task.ContextID != "" && task.Status.State == a2a.TaskStateSubmitted && len(task.History) == 1 && task.History[0].ID == message.ID, "unexpected A2A task: %#v", result)

	reservation, err := agents.runtime.ReserveInbox(context.Background(), agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation != nil && len(reservation.Messages) == 1, "unexpected inbox: %#v, %v", reservation, err)
	inbound := reservation.Messages[0]
	require(t, inbound.From == issued.Principal.ID && inbound.FromKind == MessageParticipantA2APrincipal && inbound.Body == "Review this\nFocus on security", "unexpected Bus request: %#v", inbound)

	retry, err := transport.SendMessage(context.Background(), a2aTestParams(issued.Credential, string(a2a.Version)), &a2a.SendMessageRequest{Message: message})
	require(t, err == nil && retry.(*a2a.Task).ID == task.ID, "A2A retry created different work: %#v, %v", retry, err)
}

func TestA2ASendMessageEnforcesAuthenticationVersionAndContent(t *testing.T) {
	agents := setupAgents(t, ":memory:")
	defer agents.runtime.Close()
	publication, issued := setupA2APrincipal(t, agents)
	server := httptest.NewServer(NewServer(agents.runtime, ServerOptions{}))
	defer server.Close()
	transport := a2aTestTransport(t, server, publication.ID)
	defer transport.Destroy()
	ctx := context.Background()

	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Review this"))
	message.ID = "remote-message-1"
	_, err := transport.SendMessage(ctx, a2aTestParams("invalid", string(a2a.Version)), &a2a.SendMessageRequest{Message: message})
	require(t, errors.Is(err, a2a.ErrUnauthenticated), "invalid credential error = %v", err)
	_, err = transport.SendMessage(ctx, a2aTestParams(issued.Credential, "broken"), &a2a.SendMessageRequest{Message: message})
	require(t, errors.Is(err, a2a.ErrVersionNotSupported), "invalid version error = %v", err)
	dataMessage := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"private": true}))
	dataMessage.ID = "remote-data-1"
	_, err = transport.SendMessage(ctx, a2aTestParams(issued.Credential, string(a2a.Version)), &a2a.SendMessageRequest{Message: dataMessage})
	require(t, errors.Is(err, a2a.ErrUnsupportedContentType), "unsupported content error = %v", err)
	_, err = transport.GetTask(ctx, a2aTestParams("invalid", string(a2a.Version)), &a2a.GetTaskRequest{ID: "hidden-task"})
	require(t, errors.Is(err, a2a.ErrUnauthenticated), "unauthenticated task lookup error = %v", err)
	_, err = transport.GetTask(ctx, a2aTestParams(issued.Credential, string(a2a.Version)), &a2a.GetTaskRequest{ID: "hidden-task"})
	require(t, errors.Is(err, a2a.ErrTaskNotFound), "unknown task lookup error = %v", err)

	reservation, err := agents.runtime.ReserveInbox(ctx, agents.reviewerToken, 10, 0)
	require(t, err == nil && reservation == nil, "rejected requests created work: %#v, %v", reservation, err)
}

func TestA2ASendMessageReturnsResourceLimitError(t *testing.T) {
	agents := setupAgentsWithOptions(t, ":memory:", RuntimeOptions{
		A2APrincipalMessageLimit: 10,
		A2APrincipalByteLimit:    8,
	})
	defer agents.runtime.Close()
	publication, issued := setupA2APrincipal(t, agents)
	server := httptest.NewServer(NewServer(agents.runtime, ServerOptions{}))
	defer server.Close()
	transport := a2aTestTransport(t, server, publication.ID)
	defer transport.Destroy()
	params := a2aTestParams(issued.Credential, string(a2a.Version))

	first := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("12345"))
	first.ID = "limited-1"
	if _, err := transport.SendMessage(context.Background(), params, &a2a.SendMessageRequest{Message: first}); err != nil {
		t.Fatal(err)
	}
	second := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("6789"))
	second.ID = "limited-2"
	_, err := transport.SendMessage(context.Background(), params, &a2a.SendMessageRequest{Message: second})
	require(t, errors.Is(err, a2a.ErrServerError) && strings.Contains(err.Error(), "Remote work capacity is full"), "resource limit error = %v", err)
}
