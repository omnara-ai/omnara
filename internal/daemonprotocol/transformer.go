package daemonprotocol

import (
	"encoding/json"
	"strconv"

	"github.com/omnara-ai/omnara/internal/daemonversion"
	"github.com/omnara-ai/omnara/internal/processaction"
)

func decodeFromDaemon(_ string, data []byte) (Message, error) {
	var message Message
	if err := json.Unmarshal(data, &message); err != nil {
		return Message{}, err
	}
	return message, nil
}

func encodeForDaemon(daemonVersion string, message Message) ([]byte, error) {
	if message.ActionOffer != nil && message.ActionOffer.ActionKind == ProcessActionRead &&
		!supportsLongReadWaits(daemonVersion) {
		payload, err := clampReadWait(message.ActionOffer.Payload)
		if err != nil {
			return nil, err
		}
		offer := *message.ActionOffer
		offer.Payload = payload
		message.ActionOffer = &offer
	}
	return json.Marshal(message)
}

// Daemons before processaction.LongWaitDaemonVersion reject read payloads
// whose wait_ms exceeds the legacy limit, so their reads wait at most that long.
func supportsLongReadWaits(daemonVersion string) bool {
	if daemonVersion == daemonversion.Development {
		return true
	}
	release, err := daemonversion.ParseRelease(daemonVersion)
	if err != nil {
		return false
	}
	minimum, err := daemonversion.ParseRelease(processaction.LongWaitDaemonVersion)
	if err != nil {
		return false
	}
	return daemonversion.Compare(release, minimum) >= 0
}

func clampReadWait(payload json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	var waitMS int
	if raw, ok := fields["wait_ms"]; !ok || json.Unmarshal(raw, &waitMS) != nil ||
		waitMS <= processaction.LegacyMaxWaitMilliseconds {
		return payload, nil
	}
	fields["wait_ms"] = json.RawMessage(strconv.Itoa(processaction.LegacyMaxWaitMilliseconds))
	return json.Marshal(fields)
}
