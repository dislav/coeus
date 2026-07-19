package embedder

import "github.com/openai/openai-go"

// StringInput adapts a plain string into the openai-go embedding input union.
//
// openai-go represents the `input` parameter of /embeddings as a generated
// tagged union (EmbeddingNewParamsInputUnion) whose exact discriminator field
// name varies across SDK patch versions. This file is the single place that
// needs to be reconciled with the pinned openai-go version.
//
// To discover the correct field for the installed version:
//   grep -rn "EmbeddingNewParamsInputUnion" $(go env GOMODCACHE)/github.com/openai/openai-go*
// then set `OfString` to match. The wire requirement is simply
// {"model":"<model>","input":"<text>"}.
type StringInput struct {
	Text string
}

// FromString sets the union to the given string. The concrete body matches the
// pinned SDK; if the generated union field is named differently, update only
// this method.
func (s StringInput) FromString() openai.EmbeddingNewParamsInputUnion {
	return openai.EmbeddingNewParamsInputUnion{
		OfString: openai.String(s.Text),
	}
}

// StringsInput adapts a []string into the openai-go embedding input union.
// Same union-reconciliation caveat as StringInput: the concrete field
// (OfArrayOfStrings) matches the pinned SDK version; update only here if
// the generated union changes.
type StringsInput []string

// FromStrings sets the union to the given string array. The wire requirement
// is {"model":"<model>","input":["<text>","<text>",...]}.
func (s StringsInput) FromStrings() openai.EmbeddingNewParamsInputUnion {
	return openai.EmbeddingNewParamsInputUnion{
		OfArrayOfStrings: s,
	}
}
