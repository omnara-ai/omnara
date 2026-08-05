package daemonprotocol

import "encoding/json"

func decodeFromDaemon(_ string, data []byte) (Message, error) {
	var message Message
	if err := json.Unmarshal(data, &message); err != nil {
		return Message{}, err
	}
	return message, nil
}

func encodeForDaemon(_ string, message Message) ([]byte, error) {
	return json.Marshal(message)
}
