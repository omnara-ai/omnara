package mcp

import (
	"bufio"
	"bytes"
	"io"
	"iter"
)

type sseEvent struct {
	Data        []byte
	LastEventID string
	Type        string
}

func scanSSEEvents(r io.Reader) iter.Seq2[sseEvent, error] {
	return func(yield func(sseEvent, error) bool) {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		const maxEventBytes = 4 * 1024 * 1024
		var (
			dataBuf   bytes.Buffer
			lastID    string
			eventType string
		)

		emit := func() bool {
			payload := make([]byte, dataBuf.Len())
			copy(payload, dataBuf.Bytes())
			ev := sseEvent{Data: payload, LastEventID: lastID, Type: eventType}
			dataBuf.Reset()
			eventType = ""
			return yield(ev, nil)
		}

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				if dataBuf.Len() > 0 {
					if !emit() {
						return
					}
				}
				continue
			}
			if line[0] == ':' {
				continue
			}

			field, value := splitSSEField(line)
			switch field {
			case "data":
				if dataBuf.Len() > 0 {
					dataBuf.WriteByte('\n')
				}
				if dataBuf.Len()+len(value) > maxEventBytes {
					yield(sseEvent{}, io.ErrShortBuffer)
					return
				}
				dataBuf.Write(value)
			case "event":
				eventType = string(value)
			case "id":
				lastID = string(value)
			case "retry":
			default:
			}
		}

		if dataBuf.Len() > 0 {
			if !emit() {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			yield(sseEvent{}, err)
		}
	}
}

func splitSSEField(line []byte) (string, []byte) {
	idx := bytes.IndexByte(line, ':')
	if idx < 0 {
		return string(line), nil
	}
	name := string(line[:idx])
	value := line[idx+1:]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return name, value
}
