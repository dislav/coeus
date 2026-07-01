package verifier

import (
	"encoding/json"

	"github.com/vlgrigoriev/coeus/internal/pipeline"
)

// answerDTO mirrors the extraction output's answer shape.
type answerDTO struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// questionDTO is the input shape — the questions serialized for the model.
type questionDTO struct {
	Number          int         `json:"number"`
	Question        string      `json:"question"`
	Choices         []string    `json:"choices"`
	Answers         []answerDTO `json:"answers"`
	Confidence      float64     `json:"confidence"`
	Explanation     string      `json:"explanation"`
	Tags            []string    `json:"tags,omitempty"`
}

// verificationInput is the top-level object sent to the model.
type verificationInput struct {
	Questions []questionDTO `json:"questions"`
}

// verifiedQuestionDTO is the output shape — what the model returns per question.
type verifiedQuestionDTO struct {
	Number          int         `json:"number"`
	Question        string      `json:"question"`
	Choices         []string    `json:"choices"`
	Answers         []answerDTO `json:"answers"`
	Confidence      float64     `json:"confidence"`
	Explanation     string      `json:"explanation"`
	Tags            []string    `json:"tags,omitempty"`
}

// verificationResponse is the top-level object the model returns.
type verificationResponse struct {
	Verification json.RawMessage       `json:"_verification"`
	Questions    []verifiedQuestionDTO `json:"questions"`
}

// fromPipeline converts the pipeline's ExtractedQuestion slice into the skill's
// questionDTO shape: Choices ([]Answer with ID+Text) → plain strings,
// Answers ({ID,Text}) → {id,value}. pipeline "Text" → skill "question".
func fromPipeline(questions []pipeline.ExtractedQuestion) verificationInput {
	out := verificationInput{Questions: make([]questionDTO, len(questions))}
	for i, q := range questions {
		choices := make([]string, len(q.Choices))
		for j, c := range q.Choices {
			choices[j] = c.Text
		}
		answers := make([]answerDTO, len(q.Answers))
		for j, a := range q.Answers {
			answers[j] = answerDTO{ID: a.ID, Value: a.Text}
		}
		out.Questions[i] = questionDTO{
			Number:          q.Number,
			Question:        q.Text,
			Choices:         choices,
			Answers:         answers,
			Confidence:      q.Confidence,
			Tags:            q.Tags,
		}
	}
	return out
}

// toPipeline maps the model output by position (index = position in the array,
// matching the input order). Only Confidence and Explanation are consumed into
// the pipeline's VerifiedQuestion; the rest is for the model's internal
// consistency and is captured in the raw Report. An i >= inputCount guard
// prevents mapping beyond the input range.
func toPipeline(r verificationResponse, inputCount int) pipeline.VerifyResult {
	out := pipeline.VerifyResult{Report: r.Verification}
	for i, q := range r.Questions {
		if i >= inputCount {
			break
		}
		out.Summary.Results = append(out.Summary.Results, pipeline.VerifiedQuestion{
			Index:       i,
			Confidence:  q.Confidence,
			Explanation: q.Explanation,
		})
	}
	return out
}
