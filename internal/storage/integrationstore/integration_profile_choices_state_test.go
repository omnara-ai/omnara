package integrationstore

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/stretchr/testify/require"
)

func TestIntegrationProfileChoiceStateCodec(t *testing.T) {
	base := IntegrationProfileChoiceRecord{
		IntegrationID: uuid.New(), OwnerReceiptID: uuid.New(), SourceKey: "message-1",
		Address: ConversationAddress{Kind: "thread", Ref: "channel:message"},
		Event:   json.RawMessage(`{"text":"request"}`), Payload: []byte("\x00\xfforiginal bytes\n"),
		Options: []IntegrationProfileChoiceOption{{Key: "support", ProfileID: uuid.New(), Name: "Support"}},
	}
	t.Run("published selection round trip", func(t *testing.T) {
		record := base
		record.MessageChannelID, record.MessageID = "channel", "menu"
		record.SelectedKey, record.SelectedBy = "support", "person"
		data, err := encodeIntegrationProfileChoice(record)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(data, &fields))
		require.JSONEq(t, `"menu"`, string(fields["message_id"]))
		require.JSONEq(t, `"`+record.OwnerReceiptID.String()+`"`, string(fields["owner_receipt_id"]))
		deadline := time.Now().Add(time.Hour)
		row := dbsqlc.IntegrationState{
			Kind: integrationProfileChoiceKind, Key: record.SourceKey, IntegrationID: record.IntegrationID,
			ScopeKind: &record.Address.Kind, ScopeRef: &record.Address.Ref,
			Data: data, ExpiresAt: &deadline, Revision: 2,
		}
		decoded, err := integrationProfileChoiceRecord(row)
		require.NoError(t, err)
		require.Equal(t, record.Payload, decoded.Payload)
		require.Equal(t, record.SelectedKey, decoded.SelectedKey)
		require.EqualValues(t, 2, decoded.Revision)
		fields["additional_state"] = json.RawMessage(`{"important":true}`)
		row.Data, err = json.Marshal(fields)
		require.NoError(t, err)
		_, err = integrationProfileChoiceRecord(row)
		require.ErrorContains(t, err, "unknown field", "typed replacement must not silently discard state it cannot decode")
	})
	t.Run("unpublished menu lookup", func(t *testing.T) {
		data, err := encodeIntegrationProfileChoice(base)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(data, &fields))
		require.NotContains(t, fields, "message_id")
		require.JSONEq(t, `"`+base.OwnerReceiptID.String()+`"`, string(fields["owner_receipt_id"]))
	})
	for name, mutate := range map[string]func(*IntegrationProfileChoiceRecord){
		"missing source receipt": func(r *IntegrationProfileChoiceRecord) { r.OwnerReceiptID = uuid.Nil },
		"incomplete publication": func(r *IntegrationProfileChoiceRecord) { r.MessageID = "menu" },
		"unpublished selection":  func(r *IntegrationProfileChoiceRecord) { r.SelectedKey, r.SelectedBy = "support", "person" },
		"missing selection actor": func(r *IntegrationProfileChoiceRecord) {
			r.MessageID, r.MessageChannelID, r.SelectedKey = "menu", "channel", "support"
		},
	} {
		t.Run(name, func(t *testing.T) {
			record := base
			mutate(&record)
			_, err := encodeIntegrationProfileChoice(record)
			require.Error(t, err, "the codec owns the former chooser-specific table constraints")
		})
	}
	t.Run("aggregate options bound", func(t *testing.T) {
		record := base
		record.Options = make([]IntegrationProfileChoiceOption, MaxIntegrationLaunchSlots)
		for i := range record.Options {
			record.Options[i] = IntegrationProfileChoiceOption{
				Key: string(rune('a' + i)), ProfileID: uuid.New(), Name: strings.Repeat("\x01", 512),
			}
		}
		_, err := encodeIntegrationProfileChoice(record)
		require.ErrorContains(t, err, "options exceed bounds")
	})
	t.Run("maximum bytes and HTML event fit together", func(t *testing.T) {
		record := base
		record.Payload = bytes.Repeat([]byte{0xff}, IntegrationInboxMaxPayloadBytes)
		record.Event = json.RawMessage(`{"text":"` + strings.Repeat("<&>", (IntegrationInboxMaxEventsBytes-20)/3) + `"}`)
		data, err := encodeIntegrationProfileChoice(record)
		require.NoError(t, err)
		require.Less(t, len(data), integrationStateMaxDataBytes)
		var decoded integrationProfileChoiceData
		require.NoError(t, json.Unmarshal(data, &decoded))
		require.Equal(t, record.Payload, decoded.Payload)
		require.JSONEq(t, string(record.Event), string(decoded.Event))
	})
}
