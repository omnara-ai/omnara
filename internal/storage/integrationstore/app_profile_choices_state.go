package integrationstore

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

const appProfileChoiceKind = "profile_choice"

// The chooser owns this document. Its pending-menu SQL reads owner_receipt_id,
// message_id and selected_key; change those queries together with these fields.
type appProfileChoiceData struct {
	OwnerReceiptID   uuid.UUID                `json:"owner_receipt_id"`
	Event            json.RawMessage          `json:"event"`
	Payload          []byte                   `json:"payload"`
	Options          []AppProfileChoiceOption `json:"options"`
	SelectedKey      string                   `json:"selected_key,omitempty"`
	SelectedBy       string                   `json:"selected_by,omitempty"`
	MessageChannelID string                   `json:"message_channel_id,omitempty"`
	MessageID        string                   `json:"message_id,omitempty"`
}

func encodeAppProfileChoice(record AppProfileChoiceRecord) (json.RawMessage, error) {
	if err := validateAppProfileChoiceState(record); err != nil {
		return nil, err
	}
	return encodeAppState(appProfileChoiceData{
		OwnerReceiptID: record.OwnerReceiptID, Event: record.Event, Payload: record.Payload,
		Options: record.Options, SelectedKey: record.SelectedKey, SelectedBy: record.SelectedBy,
		MessageChannelID: record.MessageChannelID, MessageID: record.MessageID,
	})
}

func appProfileChoiceRecord(row dbsqlc.AppState) (AppProfileChoiceRecord, error) {
	var result AppProfileChoiceRecord
	if row.Kind != appProfileChoiceKind || row.ScopeKind == nil || row.ScopeRef == nil || row.ExpiresAt == nil {
		return result, inboxInvalid("profile choice requires its kind, conversation and deadline")
	}
	var data appProfileChoiceData
	decoder := json.NewDecoder(bytes.NewReader(row.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		return result, fmt.Errorf("decode app profile choice: %w", err)
	}
	result = AppProfileChoiceRecord{
		ID: row.ID, ProjectID: row.ProjectID, AppID: row.AppID, SourceKey: row.Key,
		Address:  ConversationAddress{Kind: *row.ScopeKind, Ref: *row.ScopeRef},
		Revision: row.Revision, ExpiresAt: *row.ExpiresAt,
		OwnerReceiptID: data.OwnerReceiptID, Event: data.Event, Payload: data.Payload,
		Options: data.Options, SelectedKey: data.SelectedKey, SelectedBy: data.SelectedBy,
		MessageChannelID: data.MessageChannelID, MessageID: data.MessageID,
	}
	if err := validateAppProfileChoiceState(result); err != nil {
		return AppProfileChoiceRecord{}, err
	}
	return result, nil
}

func validateAppProfileChoiceState(record AppProfileChoiceRecord) error {
	if err := validateAppProfileChoice(EnsureAppProfileChoiceInput{
		AppID: record.AppID, Address: record.Address, SourceKey: record.SourceKey,
		Event: record.Event, Payload: record.Payload, Options: record.Options,
	}); err != nil {
		return err
	}
	var options bytes.Buffer
	encoder := json.NewEncoder(&options)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record.Options); err != nil {
		return err
	}
	// Bound the app's encoded options, excluding the encoder's trailing newline.
	if options.Len()-1 > 16*1024 {
		return inboxInvalid("profile choice options exceed bounds")
	}
	if record.OwnerReceiptID == uuid.Nil {
		return inboxInvalid("profile choice requires its original receipt identity")
	}
	if (record.MessageID == "") != (record.MessageChannelID == "") ||
		(record.MessageID != "" && (!choiceText(record.MessageID, 2048) || !choiceText(record.MessageChannelID, 2048))) {
		return inboxInvalid("profile choice publication requires a channel and message identity")
	}
	if (record.SelectedKey == "") != (record.SelectedBy == "") ||
		(record.SelectedKey != "" && (record.MessageID == "" ||
			!choiceText(record.SelectedKey, 64) || !choiceText(record.SelectedBy, 2048))) {
		return inboxInvalid("profile choice selection requires its published menu, option and actor")
	}
	return nil
}
