package telemlineage

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/google/uuid"
)

// The key scheme is Telem's trajectory v5, as in the Telem JS SDK
// (@telemai/sdk). Every Telem client must derive the same keys from the same
// inputs; the tests pin that against the SDK's own output.

// harness names Omnara inside every derived key.
const harness = "omnara"

// none stands in for a key component the caller does not have.
const none = "none"

// nsTrajectory is the uuid5 namespace of every client-derived key.
var nsTrajectory = uuid.MustParse("443866ab-1b45-5ed8-979e-52fdad07b810")

func sha256hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// lp length-prefixes a key component with its UTF-8 byte count, which keeps
// concatenation injective.
func lp(value string) string {
	return strconv.Itoa(len(value)) + ":" + value
}

func uuid5(name string) string {
	return uuid.NewSHA1(nsTrajectory, []byte(name)).String()
}

// fingerprint is the stable identity of one conversation across its windows.
func fingerprint(harness, conversationID string) string {
	return sha256hex(lp(harness) + lp(conversationID))
}

// sessionKey identifies one context window of one conversation.
func sessionKey(harness, conversationID, windowID string) string {
	return uuid5(lp(harness) + lp(sha256hex(conversationID)) + lp(windowID) + lp(none))
}

// eventNodeKey identifies one tool call; replays of the call derive the same key.
func eventNodeKey(harness, session, messageID, toolCallID string) string {
	return uuid5(lp(harness) + lp(session) + lp(messageID) + lp(toolCallID) + lp("event"))
}

// snapshotNodeKey identifies an agent frozen at the model call that spawned a
// subagent. Subagents spawned by one model call share it.
func snapshotNodeKey(harness, conversationID, messageID string) string {
	return uuid5(lp(harness) + lp(sha256hex(conversationID)) + lp(messageID) + lp("snap"))
}
