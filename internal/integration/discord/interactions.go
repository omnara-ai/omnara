package discord

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/publicid"
)

const (
	InteractionMaxBytes = 1024 * 1024
	InteractionTimeout  = 2 * time.Second
)

// CustomID carries only a captured Omnara interaction identity and action.
// The caller must load that interaction and revalidate live destination/actor
// authority. Neither a custom ID nor a valid Discord signature grants authority.
type CustomID struct {
	InteractionID string
	Action        string
}

func asciiToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func EncodeCustomID(id CustomID) (string, error) {
	if _, err := publicid.Decode(publicid.KindAgentInteraction, id.InteractionID); err != nil ||
		!asciiToken(id.Action) || len(id.Action) > 32 {
		return "", errors.New("invalid discord callback identity")
	}
	return "om1:" + id.InteractionID + ":" + id.Action, nil
}

func DecodeCustomID(raw string) (CustomID, error) {
	parts := strings.Split(raw, ":")
	if len(parts) != 3 || parts[0] != "om1" {
		return CustomID{}, errors.New("invalid discord callback identity")
	}
	id := CustomID{InteractionID: parts[1], Action: parts[2]}
	if _, err := EncodeCustomID(id); err != nil {
		return CustomID{}, err
	}
	return id, nil
}

type ActionRow struct {
	Type       int         `json:"type"`
	Components []Component `json:"components"`
}

// Component supports interaction buttons, profile menus and modal text inputs.
// Links, arbitrary URLs, mention selectors and executable routing data are absent.
type Component struct {
	Type        int            `json:"type"`
	Style       int            `json:"style,omitempty"`
	Label       string         `json:"label,omitempty"`
	CustomID    string         `json:"custom_id"`
	Disabled    bool           `json:"disabled,omitempty"`
	Required    *bool          `json:"required,omitempty"`
	MinLength   int            `json:"min_length,omitempty"`
	MaxLength   int            `json:"max_length,omitempty"`
	Options     []SelectOption `json:"options,omitempty"`
	Placeholder string         `json:"placeholder,omitempty"`
	MinValues   int            `json:"min_values,omitempty"`
	MaxValues   int            `json:"max_values,omitempty"`
}

type SelectOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

func validateRows(rows []ActionRow, modal bool) error {
	if len(rows) > 5 || (modal && len(rows) == 0) {
		return errors.New("invalid discord component rows")
	}
	seen := make(map[string]bool)
	for _, row := range rows {
		if row.Type != 1 || len(row.Components) == 0 || len(row.Components) > 5 ||
			(modal && len(row.Components) != 1) {
			return errors.New("invalid discord component row")
		}
		for _, component := range row.Components {
			if seen[component.CustomID] {
				return errors.New("invalid discord component")
			}
			seen[component.CustomID] = true
			if !modal && component.Type == 3 {
				if len(row.Components) != 1 {
					return errors.New("discord select must be alone in its row")
				}
				if err := validateProfileChoiceComponent(component); err != nil {
					return err
				}
				continue
			}
			if !utf8.ValidString(component.Label) || component.Label == "" ||
				len(component.Options) != 0 || component.MinValues != 0 || component.MaxValues != 0 ||
				component.Placeholder != "" {
				return errors.New("invalid discord component")
			}
			if modal {
				if component.Type != 4 || component.Style < 1 || component.Style > 2 ||
					utf8.RuneCountInString(component.Label) > 45 || !asciiToken(component.CustomID) ||
					len(component.CustomID) > 100 || component.MinLength < 0 || component.MaxLength < 1 ||
					component.MaxLength > 4000 || component.MinLength > component.MaxLength {
					return errors.New("invalid discord text input")
				}
			} else {
				if component.Type != 2 || component.Style < 1 || component.Style > 4 ||
					utf8.RuneCountInString(component.Label) > 80 {
					return errors.New("invalid discord button")
				}
				if _, err := DecodeCustomID(component.CustomID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

type Interaction struct {
	ID            string             `json:"id"`
	ApplicationID string             `json:"application_id"`
	Type          int                `json:"type"`
	GuildID       string             `json:"guild_id,omitempty"`
	ChannelID     string             `json:"channel_id,omitempty"`
	User          *User              `json:"user,omitempty"`
	Member        *InteractionMember `json:"member,omitempty"`
	Message       *Message           `json:"message,omitempty"`
	Data          InteractionData    `json:"data"`
}

type InteractionMember struct {
	User User `json:"user"`
}

type InteractionData struct {
	Name          string   `json:"name,omitempty"`
	CustomID      string   `json:"custom_id,omitempty"`
	Values        []string `json:"values,omitempty"`
	ComponentType int      `json:"component_type,omitempty"`
	// Slash options and submitted modal components remain bounded JSON for the
	// application-specific command/form decoder. Interaction tokens are omitted.
	Options    json.RawMessage `json:"options,omitempty"`
	Components json.RawMessage `json:"components,omitempty"`
}

func (i Interaction) Actor() User {
	if i.Member != nil {
		return i.Member.User
	}
	if i.User != nil {
		return *i.User
	}
	return User{}
}

type InteractionResponse struct {
	Type int                      `json:"type"`
	Data *InteractionResponseData `json:"data,omitempty"`
}

type InteractionResponseData struct {
	Content         string           `json:"content,omitempty"`
	Flags           int              `json:"flags,omitempty"`
	CustomID        string           `json:"custom_id,omitempty"`
	Title           string           `json:"title,omitempty"`
	Components      []ActionRow      `json:"components,omitzero"`
	AllowedMentions *allowedMentions `json:"allowed_mentions,omitempty"`
}

// Acknowledge returns an immediate, final acknowledgement. Components defer the
// message update; slash commands/modal submissions receive an ephemeral receipt.
func Acknowledge(interaction Interaction) InteractionResponse {
	if interaction.Type == 3 {
		return InteractionResponse{Type: 6}
	}
	return InteractionResponse{Type: 4, Data: &InteractionResponseData{Content: "Received.", Flags: 64}}
}

type InteractionIntake func(context.Context, Interaction) (InteractionResponse, error)

type InteractionHandler struct {
	applicationID string
	publicKey     ed25519.PublicKey
	intake        InteractionIntake
}

// NewInteractionHandler requires synchronous durable intake before a 2xx ack.
// The callback must honor its 2s context and deduplicate by Discord interaction
// ID. Slow/failed commits get a non-2xx response, never a false success.
func NewInteractionHandler(applicationID, publicKeyHex string, intake InteractionIntake) (*InteractionHandler, error) {
	key, err := hex.DecodeString(publicKeyHex)
	if err != nil || len(key) != ed25519.PublicKeySize || !validID(applicationID) || intake == nil {
		return nil, errors.New("invalid discord interaction handler configuration")
	}
	return &InteractionHandler{applicationID: applicationID, publicKey: key, intake: intake}, nil
}

func verifySignature(key ed25519.PublicKey, header http.Header, body []byte, now time.Time) bool {
	timestamp := header.Get("X-Signature-Timestamp")
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || len(timestamp) > 20 || len(body) > InteractionMaxBytes {
		return false
	}
	age := now.Sub(time.Unix(seconds, 0))
	if age < -5*time.Minute || age > 5*time.Minute {
		return false
	}
	signature, err := hex.DecodeString(header.Get("X-Signature-Ed25519"))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return false
	}
	message := make([]byte, 0, len(timestamp)+len(body))
	message = append(message, timestamp...)
	message = append(message, body...)
	return ed25519.Verify(key, message, signature)
}

func (h *InteractionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), InteractionTimeout)
	defer cancel()
	// Bound the read on real net/http servers as well as the callback budget.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(InteractionTimeout))
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, InteractionMaxBytes))
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	if err != nil {
		http.Error(w, "invalid interaction body", http.StatusBadRequest)
		return
	}
	if !verifySignature(h.publicKey, r.Header, body, time.Now()) {
		http.Error(w, "invalid interaction signature", http.StatusUnauthorized)
		return
	}
	var interaction Interaction
	if json.Unmarshal(body, &interaction) != nil || interaction.ApplicationID != h.applicationID ||
		!validID(interaction.ID) {
		http.Error(w, "invalid interaction", http.StatusBadRequest)
		return
	}
	response := InteractionResponse{Type: 1}
	if interaction.Type != 1 {
		if !validID(interaction.ChannelID) || !validID(interaction.Actor().ID) ||
			(interaction.GuildID != "" && !validID(interaction.GuildID)) ||
			(interaction.Type != 2 && interaction.Type != 3 && interaction.Type != 5) {
			http.Error(w, "unsupported interaction", http.StatusBadRequest)
			return
		}
		if interaction.Type == 3 || interaction.Type == 5 {
			_, callbackErr := DecodeCustomID(interaction.Data.CustomID)
			if callbackErr != nil {
				_, callbackErr = ProfileChoiceFromInteraction(interaction)
			}
			if callbackErr != nil {
				http.Error(w, "invalid callback identity", http.StatusBadRequest)
				return
			}
		}
		response, err = h.intake(ctx, interaction)
		if err != nil || ctx.Err() != nil {
			http.Error(w, "interaction intake failed", http.StatusServiceUnavailable)
			return
		}
		if !validResponse(interaction.Type, response) {
			http.Error(w, "invalid interaction response", http.StatusInternalServerError)
			return
		}
	}
	if response.Data != nil {
		data := *response.Data
		data.AllowedMentions = &allowedMentions{Parse: []string{}}
		response.Data = &data
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return // The response may already be partially written; do not append an error.
	}
}

func validResponse(interactionType int, response InteractionResponse) bool {
	if response.Type == 6 {
		return interactionType == 3 && response.Data == nil
	}
	data := response.Data
	if data == nil {
		return false
	}
	if response.Type == 9 {
		_, err := DecodeCustomID(data.CustomID)
		return interactionType != 5 && err == nil && utf8.ValidString(data.Title) &&
			data.Content == "" && data.Flags == 0 &&
			utf8.RuneCountInString(data.Title) >= 1 && utf8.RuneCountInString(data.Title) <= 45 &&
			validateRows(data.Components, true) == nil
	}
	messageResponse := (response.Type == 4 && (data.Flags == 0 || data.Flags == 64)) ||
		(response.Type == 7 && interactionType == 3 && data.Flags == 0)
	return messageResponse && data.Content != "" && utf8.ValidString(data.Content) &&
		data.Title == "" && data.CustomID == "" &&
		utf8.RuneCountInString(data.Content) <= 2000 &&
		validateRows(data.Components, false) == nil
}
