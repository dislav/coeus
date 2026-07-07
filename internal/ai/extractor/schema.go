package extractor

import (
	"encoding/json"
	"strconv"

	"github.com/invopop/jsonschema"
	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

// answerDTO mirrors the extraction output's answer shape.
type answerDTO struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// questionDTO matches the extract-questions-from-image skill wire format:
// choices are plain strings (labels stripped) and answers are {id,value}.
type questionDTO struct {
	Number          int         `json:"number"`
	Question        string      `json:"question"`
	Choices         []string    `json:"choices"`
	Answers         []answerDTO `json:"answers"`
	Confidence      float64     `json:"confidence"`
	Explanation     string      `json:"explanation"`
	Tags            []string    `json:"tags,omitempty"`
	ImageContext    string      `json:"image_context,omitempty"`
}

type extractionErrorDTO struct {
	Code               string `json:"code"`
	Message            string `json:"message"`
	Details            string `json:"details,omitempty"`
	QuestionsExtracted int    `json:"questions_extracted,omitempty"`
	QuestionsExpected  int    `json:"questions_expected,omitempty"`
}

type extractionResponse struct {
	Questions []questionDTO       `json:"questions,omitempty"`
	Error     *extractionErrorDTO `json:"error,omitempty"`
}

// extractionSchemaJSON is the JSON Schema for extractionResponse, rendered once
// at init and embedded in the user prompt as a format reminder.
var extractionSchemaJSON = schemaOf(extractionResponse{})

func schemaOf(v any) string {
	r := jsonschema.Reflector{DoNotReference: true}
	s := r.Reflect(v)
	b, err := json.Marshal(s)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// toPipeline maps the skill response to pipeline types.
//
// The skill returns choices as plain strings and answers as {id,value}. The
// pipeline's ExtractedQuestion.Choices is []Answer (with ID+Text). So we:
//  1. detect the labeling pattern from answer IDs (letters → "letter",
//     numbers → "number");
//  2. assign sequential IDs to choices based on that pattern;
//  3. map answer {id,value} to pipeline Answer{ID,Text};
//  4. map the skill's "message" to the pipeline's ExtractionError.Detail.
func toPipeline(r extractionResponse) pipeline.ExtractResult {
	res := pipeline.ExtractResult{}
	if r.Error != nil {
		res.Error = &pipeline.ExtractionError{
			Code:   r.Error.Code,
			Detail: r.Error.Message,
		}
	}
	res.Questions = make([]pipeline.ExtractedQuestion, len(r.Questions))
	for i, q := range r.Questions {
		labeling := detectChoiceLabeling(q.Answers)
		res.Questions[i] = pipeline.ExtractedQuestion{
			Number:          q.Number,
			Text:            q.Question,
			Choices:         assignChoiceIDs(q.Choices, labeling),
			Answers:         mapAnswers(q.Answers),
			Confidence:      q.Confidence,
			Tags:            q.Tags,
			ImageContext:    q.ImageContext,
		}
	}
	return res
}

// detectChoiceLabeling inspects answer IDs to determine if choices are labeled
// with letters (A, B, C…) or numbers (1, 2, 3…). Defaults to "letter".
func detectChoiceLabeling(answers []answerDTO) string {
	if len(answers) == 0 {
		return "letter"
	}
	id := answers[0].ID
	if len(id) > 0 && id[0] >= '0' && id[0] <= '9' {
		return "number"
	}
	return "letter"
}

// assignChoiceIDs creates Answer objects with sequential IDs based on the
// labeling pattern: A,B,C… for "letter", 1,2,3… for "number".
func assignChoiceIDs(choices []string, labeling string) []pipeline.Answer {
	out := make([]pipeline.Answer, len(choices))
	for i, text := range choices {
		out[i] = pipeline.Answer{
			ID:   labelFor(i, labeling),
			Text: text,
		}
	}
	return out
}

// mapAnswers converts skill {id,value} pairs to pipeline {ID,Text}.
func mapAnswers(answers []answerDTO) []pipeline.Answer {
	out := make([]pipeline.Answer, len(answers))
	for i, a := range answers {
		out[i] = pipeline.Answer{ID: a.ID, Text: a.Value}
	}
	return out
}

// labelFor returns the i-th label in the given pattern.
// labelFor(0, "letter") = "A", labelFor(1, "letter") = "B", ...
// labelFor(0, "number") = "1", labelFor(1, "number") = "2", ...
// For >26 letter-labeled choices this wraps past Z; acceptable for MVP — real
// exams rarely exceed ~10 choices.
func labelFor(i int, labeling string) string {
	switch labeling {
	case "number":
		return strconv.Itoa(i + 1)
	default:
		return string(rune('A' + i))
	}
}
