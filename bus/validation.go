package bus

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func validateIdentity(value, field string, optional bool) error {
	if value == "" && optional {
		return nil
	}
	if !identityPattern.MatchString(value) {
		return Errorf(CodeInvalidArgument, field+" must be a valid identifier")
	}
	return nil
}

func validateText(value, field string, max int, optional bool) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return Errorf(CodeInvalidArgument, field+" must be UTF-8 text without NUL characters")
	}
	if value == "" && optional {
		return nil
	}
	if strings.TrimSpace(value) == "" {
		return Errorf(CodeInvalidArgument, field+" must not be empty")
	}
	if len(value) > max {
		return Errorf(CodeInvalidArgument, fmt.Sprintf("%s exceeds %d bytes", field, max))
	}
	return nil
}

func validateLifecycle(value AgentLifecycle) error {
	switch value {
	case LifecycleStarting, LifecycleReady, LifecycleWorking, LifecycleIdle, LifecycleNeedsInput, LifecycleOffline:
		return nil
	default:
		return Errorf(CodeInvalidArgument, "lifecycle is invalid")
	}
}

func validateMessageMode(value MessageMode) error {
	switch value {
	case "", MessageNotify, MessageRequest, MessageResponse:
		return nil
	default:
		return Errorf(CodeInvalidArgument, "mode is invalid")
	}
}

func validateMessageParticipantKind(value MessageParticipantKind) error {
	switch value {
	case MessageParticipantAgent, MessageParticipantA2APrincipal, MessageParticipantHuman:
		return nil
	default:
		return Errorf(CodeInvalidArgument, "message participant kind is invalid")
	}
}

func validateCapabilities(values []AgentCapability) error {
	if len(values) > 64 {
		return Errorf(CodeInvalidArgument, "capabilities exceeds 64 items")
	}
	seen := make(map[string]bool, len(values))
	for i, capability := range values {
		if err := validateIdentity(capability.Name, fmt.Sprintf("capabilities[%d].name", i), false); err != nil {
			return err
		}
		if err := validateText(capability.Description, fmt.Sprintf("capabilities[%d].description", i), 512, true); err != nil {
			return err
		}
		if seen[capability.Name] {
			return Errorf(CodeInvalidArgument, "capability names must be unique")
		}
		seen[capability.Name] = true
	}
	return nil
}

func validateContext(values []ContextItem) error {
	if len(values) > 32 {
		return Errorf(CodeInvalidArgument, "context exceeds 32 items")
	}
	total := 0
	for i, item := range values {
		switch item.Kind {
		case "text", "file", "url", "reference":
		default:
			return Errorf(CodeInvalidArgument, fmt.Sprintf("context[%d].kind is invalid", i))
		}
		if err := validateText(item.Title, fmt.Sprintf("context[%d].title", i), 512, false); err != nil {
			return err
		}
		if err := validateText(item.Text, fmt.Sprintf("context[%d].text", i), 65536, true); err != nil {
			return err
		}
		if err := validateText(item.URI, fmt.Sprintf("context[%d].uri", i), 4096, true); err != nil {
			return err
		}
		total += len(item.Title) + len(item.Text) + len(item.URI) + len(item.MediaType)
	}
	if total > 262144 {
		return Errorf(CodeInvalidArgument, "context exceeds 256 KiB")
	}
	return nil
}

func normalizedLease(value int64) (int64, error) {
	if value == 0 {
		return 300000, nil
	}
	if value < 30000 || value > 86400000 {
		return 0, Errorf(CodeInvalidArgument, "leaseMs must be between 30000 and 86400000")
	}
	return value, nil
}
