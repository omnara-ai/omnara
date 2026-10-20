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
	if message.ActionOffer != nil && message.ActionOffer.ActionKind == ProcessActionRead {
		payload := message.ActionOffer.Payload
		var err error
		if !supportsLongReadWaits(daemonVersion) {
			if payload, err = clampReadWait(payload); err != nil {
				return nil, err
			}
		}
		if !supportsOutputWindows(daemonVersion) {
			if payload, err = withoutOutputWait(payload); err != nil {
				return nil, err
			}
		}
		offer := *message.ActionOffer
		offer.Payload = payload
		message.ActionOffer = &offer
	}
	return json.Marshal(message)
}

// Daemons before 0.2.6 reject read waits over 10s; remove this clamp once they are unsupported.
func supportsLongReadWaits(daemonVersion string) bool {
	return daemonAtLeast(daemonVersion, processaction.LongWaitDaemonVersion)
}

// Daemons before 0.2.9 reject the output-window read fields; without them a
// read returns on the first output, as it always did. Process offers need no
// change because daemons ignore unknown offer fields.
func supportsOutputWindows(daemonVersion string) bool {
	return daemonAtLeast(daemonVersion, processaction.OutputWindowDaemonVersion)
}

func daemonAtLeast(daemonVersion, minimumVersion string) bool {
	if daemonVersion == daemonversion.Development {
		return true
	}
	release, err := daemonversion.ParseRelease(daemonVersion)
	if err != nil {
		return false
	}
	minimum, err := daemonversion.ParseRelease(minimumVersion)
	if err != nil {
		return false
	}
	return daemonversion.Compare(release, minimum) >= 0
}

func withoutOutputWait(payload json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	removed := false
	for _, name := range []string{"wait_for_exit", "quiet_ms", "head_bytes"} {
		if _, ok := fields[name]; ok {
			delete(fields, name)
			removed = true
		}
	}
	if !removed {
		return payload, nil
	}
	return json.Marshal(fields)
}

func clampReadWait(payload json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	raw, ok := fields["wait_ms"]
	if !ok {
		return payload, nil
	}
	var waitMS int
	if err := json.Unmarshal(raw, &waitMS); err != nil {
		return nil, err
	}
	if waitMS <= processaction.LegacyMaxWaitMilliseconds {
		return payload, nil
	}
	fields["wait_ms"] = json.RawMessage(strconv.Itoa(processaction.LegacyMaxWaitMilliseconds))
	return json.Marshal(fields)
}
