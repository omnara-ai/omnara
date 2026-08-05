package daemonprotocol

import (
	"context"

	"github.com/coder/websocket"
)

type BackendSocket struct {
	conn          *websocket.Conn
	daemonVersion string
}

func NewBackendSocket(conn *websocket.Conn, daemonVersion string) *BackendSocket {
	return &BackendSocket{conn: conn, daemonVersion: daemonVersion}
}

func (s *BackendSocket) Read(ctx context.Context) (Message, error) {
	_, data, err := s.conn.Read(ctx)
	if err != nil {
		return Message{}, err
	}
	message, err := decodeFromDaemon(s.daemonVersion, data)
	if err != nil {
		_ = s.conn.Close(websocket.StatusInvalidFramePayloadData, "failed to unmarshal JSON")
		return Message{}, err
	}
	return message, nil
}

func (s *BackendSocket) Write(ctx context.Context, message Message) error {
	data, err := encodeForDaemon(s.daemonVersion, message)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageText, data)
}

func (s *BackendSocket) Ping(ctx context.Context) error {
	return s.conn.Ping(ctx)
}

func (s *BackendSocket) Close(code websocket.StatusCode, reason string) error {
	return s.conn.Close(code, reason)
}
