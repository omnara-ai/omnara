package interactionform

type RenderedAnswers struct {
	Answers []RenderedAnswer `json:"answers"`
}

type RenderedAnswer struct {
	QuestionIndex   int                    `json:"question_index"`
	Question        string                 `json:"question"`
	SelectedOptions []SelectedAnswerOption `json:"selected_options"`
	Text            string                 `json:"text,omitempty"`
}

type SelectedAnswerOption struct {
	OptionIndex int    `json:"option_index"`
	Label       string `json:"label"`
}

func RenderAnswers(
	form Form,
	resolution Resolution,
) RenderedAnswers {
	result := RenderedAnswers{
		Answers: make([]RenderedAnswer, 0, len(resolution.Answers)),
	}
	for questionIndex, answer := range resolution.Answers {
		question := form.Questions[questionIndex]
		selectedOptions := make(
			[]SelectedAnswerOption,
			0,
			len(answer.OptionIndices),
		)
		for _, optionIndex := range answer.OptionIndices {
			selectedOptions = append(selectedOptions, SelectedAnswerOption{
				OptionIndex: optionIndex,
				Label:       question.Options[optionIndex].Label,
			})
		}
		result.Answers = append(result.Answers, RenderedAnswer{
			QuestionIndex:   questionIndex,
			Question:        question.Prompt,
			SelectedOptions: selectedOptions,
			Text:            answer.Text,
		})
	}
	return result
}
