package interactionform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type Form struct {
	Title     string        `json:"title"`
	Context   []ContextItem `json:"context,omitempty"`
	Questions []Question    `json:"questions"`
}

type ContextItem struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type Question struct {
	Prompt   string   `json:"prompt"`
	Multiple bool     `json:"multiple,omitempty"`
	Options  []Option `json:"options"`
}

type Option struct {
	Label      string `json:"label"`
	AllowsText bool   `json:"allows_text,omitempty"`
}

type Resolution struct {
	Answers []Answer `json:"answers"`
}

type Answer struct {
	OptionIndices []int  `json:"option_indices"`
	Text          string `json:"text,omitempty"`
}

func New(
	title string,
	contextItems []ContextItem,
	questions []Question,
) (Form, error) {
	value := Form{
		Title:     title,
		Context:   append([]ContextItem(nil), contextItems...),
		Questions: cloneQuestions(questions),
	}
	if err := value.Validate(); err != nil {
		return Form{}, err
	}
	return value, nil
}

func Parse(raw json.RawMessage) (Form, error) {
	var value Form
	if err := decodeStrictJSON(raw, &value); err != nil {
		return Form{}, fmt.Errorf("decode interaction form: %w", err)
	}
	if err := value.Validate(); err != nil {
		return Form{}, err
	}
	return value, nil
}

func (value *Form) Validate() error {
	value.Title = strings.TrimSpace(value.Title)
	if value.Title == "" {
		return errors.New("interaction form title is required")
	}
	for index := range value.Context {
		item := &value.Context[index]
		item.Label = strings.TrimSpace(item.Label)
		item.Value = strings.TrimSpace(item.Value)
		if item.Label == "" || item.Value == "" {
			return errors.New("interaction form context requires labels and values")
		}
	}
	if len(value.Questions) == 0 {
		return errors.New("interaction form requires at least one question")
	}
	for questionIndex := range value.Questions {
		question := &value.Questions[questionIndex]
		question.Prompt = strings.TrimSpace(question.Prompt)
		if question.Prompt == "" {
			return fmt.Errorf("interaction form question %d requires a prompt", questionIndex)
		}
		if len(question.Options) == 0 {
			return fmt.Errorf("interaction form question %d requires at least one option", questionIndex)
		}
		for optionIndex := range question.Options {
			option := &question.Options[optionIndex]
			option.Label = strings.TrimSpace(option.Label)
			if option.Label == "" {
				return fmt.Errorf(
					"interaction form question %d option %d requires a label",
					questionIndex,
					optionIndex,
				)
			}
		}
	}
	return nil
}

func ParseResolution(value Form, raw json.RawMessage) (Resolution, error) {
	var resolution Resolution
	if err := decodeStrictJSON(raw, &resolution); err != nil {
		return Resolution{}, fmt.Errorf("decode interaction resolution: %w", err)
	}
	return NormalizeResolution(value, resolution)
}

func NormalizeResolution(value Form, resolution Resolution) (Resolution, error) {
	if err := value.Validate(); err != nil {
		return Resolution{}, err
	}
	if len(resolution.Answers) != len(value.Questions) {
		return Resolution{}, fmt.Errorf(
			"interaction resolution requires exactly %d answers",
			len(value.Questions),
		)
	}
	normalized := Resolution{Answers: append([]Answer(nil), resolution.Answers...)}
	for answerIndex := range normalized.Answers {
		answer := normalized.Answers[answerIndex]
		answer.Text = strings.TrimSpace(answer.Text)
		if len(answer.OptionIndices) == 0 {
			return Resolution{}, fmt.Errorf(
				"interaction answer %d requires at least one selected option",
				answerIndex,
			)
		}
		question := value.Questions[answerIndex]
		if !question.Multiple && len(answer.OptionIndices) != 1 {
			return Resolution{}, fmt.Errorf(
				"interaction form question %d requires exactly one option",
				answerIndex,
			)
		}
		selectedIndices := make(map[int]struct{}, len(answer.OptionIndices))
		for _, optionIndex := range answer.OptionIndices {
			if optionIndex < 0 || optionIndex >= len(question.Options) {
				return Resolution{}, fmt.Errorf(
					"interaction form question %d has no option at index %d",
					answerIndex,
					optionIndex,
				)
			}
			if _, duplicate := selectedIndices[optionIndex]; duplicate {
				return Resolution{}, fmt.Errorf(
					"interaction form question %d selects option %d more than once",
					answerIndex,
					optionIndex,
				)
			}
			selectedIndices[optionIndex] = struct{}{}
		}
		answer.OptionIndices = make([]int, 0, len(selectedIndices))
		allowsText := false
		for optionIndex, option := range question.Options {
			if _, selected := selectedIndices[optionIndex]; !selected {
				continue
			}
			answer.OptionIndices = append(answer.OptionIndices, optionIndex)
			allowsText = allowsText || option.AllowsText
		}
		if !allowsText && answer.Text != "" {
			return Resolution{}, fmt.Errorf(
				"interaction form question %d selected options do not accept text",
				answerIndex,
			)
		}
		normalized.Answers[answerIndex] = answer
	}
	return normalized, nil
}

func cloneQuestions(questions []Question) []Question {
	out := make([]Question, len(questions))
	for index, question := range questions {
		out[index] = Question{
			Prompt:   question.Prompt,
			Multiple: question.Multiple,
			Options:  append([]Option(nil), question.Options...),
		}
	}
	return out
}

func decodeStrictJSON(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("JSON value is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
