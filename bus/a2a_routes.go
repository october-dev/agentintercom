package bus

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/october-dev/october-bus/a2abridge"
)

func (s *Server) agentCardURLs(publicationID string) (string, string, error) {
	base := strings.TrimRight(strings.TrimSpace(s.options.PublicBaseURL), "/")
	if base == "" {
		base = strings.TrimRight(s.address, "/")
	}
	if base == "" {
		return "", "", Errorf(CodeInternal, "Public server address is not available")
	}
	pathID := url.PathEscape(publicationID)
	interfaceURL := base + "/a2a/agents/" + pathID
	if _, err := a2abridge.NewAgentCard(a2abridge.AgentProfile{DisplayName: "Agent"}, a2abridge.CardOptions{
		InterfaceURL: interfaceURL,
		Version:      Version,
	}); err != nil {
		return "", "", Errorf(CodeInternal, "Public server address is invalid")
	}
	return interfaceURL + "/.well-known/agent-card.json", interfaceURL, nil
}

func (s *Server) publicationResult(publication agentCardPublication) (AgentCardPublication, error) {
	cardURL, interfaceURL, err := s.agentCardURLs(publication.ID)
	if err != nil {
		return AgentCardPublication{}, err
	}
	return AgentCardPublication{
		ID: publication.ID, ScopeID: publication.ScopeID, AgentID: publication.AgentID, Enabled: publication.Enabled,
		CardURL: cardURL, InterfaceURL: interfaceURL, CreatedAt: instant(publication.CreatedAt), UpdatedAt: instant(publication.UpdatedAt),
	}, nil
}

func (s *Server) createAgentCardPublication(response http.ResponseWriter, request *http.Request) error {
	token, err := bearer(request)
	if err != nil {
		return err
	}
	if _, _, err := s.agentCardURLs("publication"); err != nil {
		return err
	}
	var input PublishAgentCardInput
	if err := decodeBody(response, request, &input); err != nil {
		return err
	}
	publication, err := s.runtime.CreateAgentCardPublication(request.Context(), token, input)
	if err != nil {
		return err
	}
	result, err := s.publicationResult(publication)
	if err != nil {
		return err
	}
	writeResult(response, http.StatusCreated, result)
	return nil
}

func (s *Server) listAgentCardPublications(response http.ResponseWriter, request *http.Request) error {
	token, err := bearer(request)
	if err != nil {
		return err
	}
	publications, err := s.runtime.ListAgentCardPublications(request.Context(), token)
	if err != nil {
		return err
	}
	result := make([]AgentCardPublication, 0, len(publications))
	for _, publication := range publications {
		value, err := s.publicationResult(publication)
		if err != nil {
			return err
		}
		result = append(result, value)
	}
	writeResult(response, http.StatusOK, result)
	return nil
}

func (s *Server) setAgentCardPublication(response http.ResponseWriter, request *http.Request, enabled bool) error {
	token, err := bearer(request)
	if err != nil {
		return err
	}
	var input emptyInput
	if err := decodeBody(response, request, &input); err != nil {
		return err
	}
	publication, err := s.runtime.SetAgentCardPublicationEnabled(request.Context(), token, request.PathValue("publicationId"), enabled)
	if err != nil {
		return err
	}
	result, err := s.publicationResult(publication)
	if err != nil {
		return err
	}
	writeResult(response, http.StatusOK, result)
	return nil
}

func (s *Server) enableAgentCardPublication(response http.ResponseWriter, request *http.Request) error {
	return s.setAgentCardPublication(response, request, true)
}

func (s *Server) disableAgentCardPublication(response http.ResponseWriter, request *http.Request) error {
	return s.setAgentCardPublication(response, request, false)
}

func (s *Server) servePublishedAgentCard(response http.ResponseWriter, request *http.Request) error {
	publication, agent, err := s.runtime.store.PublishedAgent(request.Context(), request.PathValue("publicationId"))
	if err != nil {
		return err
	}
	_, interfaceURL, err := s.agentCardURLs(publication.ID)
	if err != nil {
		return err
	}
	capabilities := make([]a2abridge.Capability, 0, len(agent.Capabilities))
	for _, capability := range agent.Capabilities {
		capabilities = append(capabilities, a2abridge.Capability{Name: capability.Name, Description: capability.Description})
	}
	card, err := a2abridge.NewAgentCard(a2abridge.AgentProfile{
		DisplayName: agent.DisplayName, Capabilities: capabilities,
	}, a2abridge.CardOptions{InterfaceURL: interfaceURL, Version: Version})
	if err != nil {
		return err
	}
	handler, err := a2abridge.NewAgentCardHandlerWithOptions(card, a2abridge.HandlerOptions{
		CacheLifetime: 0,
		LastModified:  time.UnixMilli(publication.UpdatedAt),
	})
	if err != nil {
		return err
	}
	handler.ServeHTTP(response, request)
	return nil
}
